"""Isolated R2 verification. No registry or tag API calls leave this process."""
from hashlib import sha256
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
from unittest.mock import patch

MANIFEST = Path("container-pair.json")
SCRIPT = "scripts/fork-release/container-pair.py"
REPOSITORY = "fixture-owner/multica"
TAG = "v1.2.3"
OLDER = "v1.2.2"
REVISION = "a" * 40
INDEXES = {}
CONFIGS = {}
ALIASES = {}
WRITES = []


def image(role, tag, variant):
    repo = f"ghcr.io/{REPOSITORY}-{role}"
    revision = REVISION if tag == TAG else "b" * 40
    children = {}
    manifests = []
    for arch in ("amd64", "arm64"):
        digest = "sha256:" + sha256(f"{role}/{arch}/{tag}/{variant}".encode()).hexdigest()
        children[arch] = digest
        manifests.append({"digest": digest, "platform": {"os": "linux", "architecture": arch}})
        CONFIGS[f"{repo}@{digest}"] = json.dumps({
            "os": "linux", "architecture": arch, "config": {"Labels": {
                "org.opencontainers.image.version": tag,
                "org.opencontainers.image.revision": revision,
            }},
        }).encode()
    raw = json.dumps({"manifests": manifests}).encode()
    digest = "sha256:" + sha256(raw).hexdigest()
    INDEXES[f"{repo}@{digest}"] = raw
    return dict(repository=repo, digest=digest, platforms=children, tag=tag, revision=revision)


def mock_read(args):
    *prefix, ref = args
    if prefix == ["skopeo", "inspect", "--raw"]:
        ref = ref.removeprefix("docker://")
        return INDEXES[ALIASES.get(ref, ref)]
    if prefix == ["skopeo", "inspect", "--config"]:
        return CONFIGS[ref.removeprefix("docker://")]
    if prefix == ["skopeo", "list-tags"]:
        repo = ref.removeprefix("docker://") + ":"
        return json.dumps({"Tags": [key.removeprefix(repo) for key in ALIASES if key.startswith(repo)]}).encode()
    if args == ("gh", "api", f"repos/{REPOSITORY}/commits/{TAG}"):
        return json.dumps({"sha": REVISION}).encode()
    if args == ("gh", "api", "--paginate", "--slurp", f"repos/{REPOSITORY}/tags?per_page=100"):
        return json.dumps([[{"name": OLDER}, {"name": TAG}]]).encode()
    raise AssertionError(args)


def mock_write(args, *, check):
    *prefix, destination, source = args
    assert check and prefix == ["docker", "buildx", "imagetools", "create", "-t"], args
    assert source in INDEXES, source
    WRITES.append((destination, source))
    ALIASES[destination] = source


def invoke_cli():
    with (
        patch.dict(os.environ, GITHUB_REF_NAME=TAG, GITHUB_SHA=REVISION, GITHUB_REPOSITORY=REPOSITORY),
        patch.object(sys, "argv", [SCRIPT, "promote"]),
        patch.object(subprocess, "check_output", side_effect=mock_read),
        patch.object(subprocess, "run", side_effect=mock_write),
    ):
        runpy.run_path(SCRIPT, run_name="__main__")


backend, web = (image(role, TAG, "original") for role in ("backend", "web"))
original = dict(run=os.environ["GITHUB_RUN_ID"], attempt="1", images={"backend": backend, "web": web})
backend_repo, web_repo = backend["repository"], web["repository"]
backend_source, web_source = (f"{item['repository']}@{item['digest']}" for item in (backend, web))
web_older = image("web", OLDER, "previous")
web_rebuilt = image("web", TAG, "rebuilt")
ALIASES.update({
    f"{backend_repo}:{TAG}": backend_source,
    f"{backend_repo}:latest": backend_source,
    f"{web_repo}:latest": f"{web_repo}@{web_older['digest']}",
})

mode = sys.argv[1]
if mode == "seal":
    assert os.environ["GITHUB_RUN_ATTEMPT"] == "1"
    MANIFEST.write_text(json.dumps(original, indent=2))
    print(f"Original run={original['run']} attempt=1 manifest_sha256={sha256(MANIFEST.read_bytes()).hexdigest()}")
elif mode == "red":
    nested = MANIFEST.parent / "container-pair" / MANIFEST
    assert json.loads(nested.read_text()) == original and not MANIFEST.exists()
    before = ALIASES.copy()
    try:
        invoke_cli()
    except FileNotFoundError as err:
        assert Path(err.filename) == MANIFEST
        assert not WRITES and ALIASES == before
        print(f"RED reproduced: {nested} exists, CLI cannot open {MANIFEST}; registry writes=0")
    else:
        raise AssertionError("baseline did not fail at the missing manifest")
elif mode == "green":
    assert os.environ["GITHUB_RUN_ATTEMPT"] == "2"
    assert json.loads(MANIFEST.read_text()) == original
    assert not (MANIFEST.parent / "container-pair" / MANIFEST).exists()
    assert web["digest"] != web_rebuilt["digest"]
    print(f"Restored run={original['run']} sealed_attempt=1 manifest_sha256={sha256(MANIFEST.read_bytes()).hexdigest()}")
    invoke_cli()
    for alias in (TAG, "latest"):
        assert ALIASES[f"{backend_repo}:{alias}"] == backend_source
        assert ALIASES[f"{web_repo}:{alias}"] == web_source
    assert WRITES == [(f"{web_repo}:{alias}", web_source) for alias in (TAG, "latest")], WRITES
    print(f"Backend stable tag/latest unchanged: {backend['digest']}")
    for dest, src in WRITES:
        print(f"One missing Web alias write: {dest} <= {src}")
    print(f"Web rebuild excluded: {web_rebuilt['digest']}")
    before = ALIASES.copy()
    WRITES.clear()
    invoke_cli()
    assert not WRITES and ALIASES == before
    print("Re-run registry writes=0; aliases unchanged")
else:
    raise SystemExit(f"unexpected mode: {mode}")
