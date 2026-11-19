"""Seal and promote an amd64/arm64 backend and web release pair."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

STABLE = re.compile(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
PLATFORMS = {"amd64", "arm64"}


def run(*args):
    return subprocess.check_output(args)


def version(tag):
    match = STABLE.fullmatch(tag)
    if not match:
        raise ValueError(f"not a stable version: {tag}")
    return tuple(map(int, match.groups()))


def inspect(image):
    # Skopeo preserves the registry bytes for --raw. Hash those bytes to pin
    # the index before looking at any of its children.
    data = run("skopeo", "inspect", "--raw", "docker://" + image)
    index = "sha256:" + hashlib.sha256(data).hexdigest()
    repo = image.rpartition("@")[0] if "@" in image else image.rsplit(":", 1)[0]
    children = {}
    labels = set()
    for descriptor in json.loads(data)["manifests"]:
        platform = descriptor.get("platform", {})
        if (
            platform.get("os") == "unknown"
            and descriptor.get("annotations", {}).get("vnd.docker.reference.type")
            == "attestation-manifest"
        ):
            continue
        arch = platform.get("architecture")
        child = descriptor["digest"]
        if (
            platform.get("os") != "linux"
            or arch not in PLATFORMS
            or arch in children
            or not DIGEST.fullmatch(child)
        ):
            raise ValueError(f"unexpected platform in {image}: {platform}")
        config = json.loads(run("skopeo", "inspect", "--config", f"docker://{repo}@{child}"))
        if config["os"] != "linux" or config["architecture"] != arch:
            raise ValueError(f"{child} platform mismatch")
        meta = config["config"]["Labels"]
        labels.add(
            (meta["org.opencontainers.image.version"], meta["org.opencontainers.image.revision"])
        )
        children[arch] = child
    if set(children) != PLATFORMS or len(labels) != 1:
        raise ValueError(f"inconsistent index: {image}")
    tag, revision = labels.pop()
    version(tag)
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError(f"{image} lacks a full revision")
    return dict(repository=repo, digest=index, platforms=children, tag=tag, revision=revision)


def registry_tags(repo):
    # Only a successful, valid registry listing can establish tag absence.
    tags = json.loads(run("skopeo", "list-tags", "docker://" + repo))["Tags"]
    if not isinstance(tags, list) or not all(isinstance(tag, str) for tag in tags):
        raise ValueError(f"invalid registry tag list for {repo}")
    return tags


def retain(tag, revision, run_id, attempt, owner, folder):
    version(tag)
    repositories = {role: f"ghcr.io/{owner}/multica-{role}" for role in ("backend", "web")}
    # This path is used only when the run's sealed artifact is missing. Once
    # either image is public, rebuilt bytes cannot establish the original pair.
    for image in repositories.values():
        tags = registry_tags(image)
        if tag in tags or ("latest" in tags and inspect(f"{image}:latest")["tag"] == tag):
            raise ValueError(f"{image} already exposes {tag}, but the sealed manifest is missing")

    result = {}
    for role, image in repositories.items():
        parts = sorted(p.name for p in (folder / role).iterdir())
        if len(parts) != 2 or not all(re.fullmatch("[0-9a-f]{64}", p) for p in parts):
            raise ValueError(f"missing platform digests for {role}")
        # This temporary tag keeps the sealed index addressable until promotion.
        staging = f"{image}:candidate-{run_id}-{attempt}"
        subprocess.run(
            [
                "docker",
                "buildx",
                "imagetools",
                "create",
                "-t",
                staging,
                *[f"{image}@sha256:{p}" for p in parts],
            ],
            check=True,
        )
        item = inspect(staging)
        if (item["tag"], item["revision"]) != (tag, revision) or inspect(
            f"{image}@{item['digest']}"
        ) != item:
            raise ValueError(f"staged {role} does not match this build")
        result[role] = item
    Path("container-pair.json").write_text(
        json.dumps(dict(run=run_id, attempt=attempt, images=result), indent=2)
    )


def promote(manifest, tag, revision, repository):
    pair = json.loads(manifest.read_text())["images"]
    if set(pair) != {"backend", "web"}:
        raise ValueError("incomplete release manifest")
    latest = {}
    existing = {}
    for role, sealed in pair.items():
        repo = f"ghcr.io/{repository.lower()}-{role}"
        if sealed["repository"] != repo or sealed["tag"] != tag or sealed["revision"] != revision:
            raise ValueError(f"{role} does not match tag, repository, or workflow revision")
        if inspect(f"{repo}@{sealed['digest']}") != sealed:
            raise ValueError(f"{role} differs from retained manifest")
        existing[role] = tag in registry_tags(repo)
        if existing[role] and inspect(f"{repo}:{tag}") != sealed:
            raise ValueError(f"{role} version tag already points to different bytes")
        latest[role] = inspect(f"{repo}:latest")
        current = latest[role]
        if version(current["tag"]) > version(tag) or (current["tag"] == tag and current != sealed):
            raise ValueError(f"{role} latest is newer or has drifted")
    remote = json.loads(run("gh", "api", f"repos/{repository}/commits/{tag}"))
    if remote["sha"] != revision:
        raise ValueError("remote tag revision changed")
    pages = json.loads(
        run("gh", "api", "--paginate", "--slurp", f"repos/{repository}/tags?per_page=100")
    )
    highest = max(version(t["name"]) for page in pages for t in page if STABLE.fullmatch(t["name"]))
    if version(tag) != highest:
        print(f"Skipping older release {tag}")
        return
    for alias in (tag, "latest"):
        for role, sealed in pair.items():
            repo = sealed["repository"]
            if not (existing[role] if alias == tag else latest[role] == sealed):
                subprocess.run(
                    [
                        "docker",
                        "buildx",
                        "imagetools",
                        "create",
                        "-t",
                        f"{repo}:{alias}",
                        f"{repo}@{sealed['digest']}",
                    ],
                    check=True,
                )
            if inspect(f"{repo}:{alias}") != sealed:
                raise ValueError(f"{role}:{alias} verification failed")


def consumer_inputs(repository, previous):
    """Called with the consumer's upgrade lock held, before any container starts."""
    pair = {}
    services = {}
    for role, service in [("backend", "backend"), ("web", "frontend")]:
        repo = f"ghcr.io/{repository.lower()}-{role}"
        item = inspect(f"{repo}:latest")
        if item != inspect(f"{repo}:{item['tag']}"):
            raise ValueError(f"{role}:latest differs from its stable version")
        old = previous[role]
        if version(item["tag"]) < version(old["tag"]) or (
            item["tag"] == old["tag"] and item["digest"] != old["digest"]
        ):
            raise ValueError(f"{role}:latest regressed or changed in place")
        pair[role] = item
        services[service] = {"image": f"{repo}@{item['digest']}"}
    if len({(item["tag"], item["revision"]) for item in pair.values()}) != 1:
        raise ValueError("backend and web are from different releases")
    return pair, {"services": services}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["retain", "promote", "pin"])
    parser.add_argument("--last-successful", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--repository", default="iTradingAI/multica")
    args = parser.parse_args()
    if args.action == "pin":
        if args.last_successful is None or args.output is None:
            parser.error("pin requires --last-successful and --output")
        pair, override = consumer_inputs(
            args.repository, json.loads(args.last_successful.read_text())
        )
        args.output.mkdir(exist_ok=False)
        (args.output / "pair.json").write_text(json.dumps(pair, indent=2))
        (args.output / "compose.json").write_text(json.dumps(override, indent=2))
        raise SystemExit(0)
    tag, revision, repository = [
        os.environ[k] for k in ("GITHUB_REF_NAME", "GITHUB_SHA", "GITHUB_REPOSITORY")
    ]
    if args.action == "retain":
        retain(
            tag,
            revision,
            os.environ["GITHUB_RUN_ID"],
            os.environ["GITHUB_RUN_ATTEMPT"],
            repository.split("/")[0].lower(),
            Path("digests"),
        )
    else:
        promote(Path("container-pair.json"), tag, revision, repository)
