"""Publish a complete, verified four-platform release from retained CI artifacts."""

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.request

import yaml

from linux_appimage_abi import RECORD, verify_record


CHANNELS = {
    "desktop-win-x64": ("latest.yml", "windows-x64", ["exe"]),
    "desktop-linux-x64": ("latest-linux.yml", "linux", ["AppImage"]),
    "desktop-mac-arm64": ("latest-mac.yml", "mac-arm64", ["dmg", "zip"]),
    "desktop-mac-x64": ("latest-x64-mac.yml", "mac-x64", ["dmg", "zip"]),
}


def stable(tag):
    if not re.fullmatch(r"v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)", tag):
        raise ValueError(f"expected a stable release tag, got {tag!r}")
    return tuple(map(int, tag[1:].split(".")))


def digest(stream):
    result = hashlib.sha512()
    while chunk := stream.read(1024 * 1024):
        result.update(chunk)
    return base64.b64encode(result.digest()).decode()


def inventory(root, tag, sha):
    stable(tag)
    files = {}
    for target, (channel, platform, required) in CHANNELS.items():
        folder = root / target
        if (folder / "build-revision").read_text().strip() != sha:
            raise ValueError(f"{target} revision mismatch")
        sidecars = {"build-revision"}
        if target == "desktop-linux-x64":
            verify_record(folder, sha, tag[1:])
            sidecars.add(RECORD)
        assets = {p.name: p for p in folder.iterdir() if p.name not in sidecars}
        meta = yaml.safe_load(assets[channel].read_bytes())
        prefix = f"multica-desktop-{tag[1:]}-{platform}"
        if meta["version"] != tag[1:] or (
            platform.startswith("mac") and meta.get("minimumSystemVersion") != "22.0.0"
        ):
            raise ValueError(f"{channel} version or OS floor mismatch")
        suffixes = {
            "windows-x64": [".exe", ".exe.blockmap"],
            "linux": ["-x86_64.AppImage", "-x86_64.rpm", "-amd64.deb"],
            "mac-x64": [".zip", ".zip.blockmap", ".dmg", ".dmg.blockmap"],
            "mac-arm64": [".zip", ".zip.blockmap", ".dmg", ".dmg.blockmap"],
        }[platform]
        expected = {prefix + suffix for suffix in suffixes} | {channel}
        if set(assets) != expected:
            raise ValueError(f"{target} unexpected or missing files: {set(assets) ^ expected}")
        hashes = {}
        for name, path in assets.items():
            with path.open("rb") as stream:
                hashes[name] = digest(stream)
            if path.stat().st_size == 0:
                raise ValueError(f"{name} is empty")
            files[name] = (path, hashes[name])
        refs = set()
        for entry in meta["files"]:
            name = entry["url"]
            if (
                name in refs
                or name not in assets
                or hashes[name] != entry["sha512"]
                or assets[name].stat().st_size != entry["size"]
            ):
                raise ValueError(f"{channel} invalid reference {name}")
            refs.add(name)
        if not all(any(name.endswith("." + ext) for name in refs) for ext in required):
            raise ValueError(f"{channel} missing installer")
        if meta["path"] not in refs or hashes[meta["path"]] != meta["sha512"]:
            raise ValueError(f"{channel} legacy update entry mismatch")
    return files


def api(path, *args):
    raw = subprocess.check_output(["gh", "api", *args, path])
    return json.loads(raw) if raw.strip() else None


def pages(path):
    return [item for page in api(path, "--paginate", "--slurp") for item in page]


def checked_tag(repo, tag, sha):
    commit = api(f"{repo}/commits/{tag}")["sha"]
    if commit != sha:
        raise ValueError(f"{tag} now points at {commit}")


def remote_hash(url):
    with subprocess.Popen(
        ["gh", "api", "-H", "Accept: application/octet-stream", url], stdout=subprocess.PIPE
    ) as cmd:
        result = digest(cmd.stdout)
        if cmd.wait():
            raise RuntimeError("failed reading uploaded asset")
    return result


def verify_public_release(repository, tag, files):
    url = f"https://api.github.com/repos/{repository}/releases/latest"
    with urllib.request.urlopen(url, timeout=30) as stream:
        latest = json.load(stream)["tag_name"]
    if stable(latest) > stable(tag):
        print(f"{tag} is published and superseded by {latest}")
        return
    if latest != tag:
        raise RuntimeError(f"anonymous latest lookup differs: expected {tag}, got {latest}")
    for name in files:
        url = f"https://github.com/{repository}/releases/download/{tag}/{name}"
        request = urllib.request.Request(url, method="HEAD")
        with urllib.request.urlopen(request, timeout=30) as stream:
            if stream.status != 200:
                raise RuntimeError(f"{name} isn't publicly downloadable")
    print(f"{tag} verified as Latest with all assets publicly downloadable")


def publish(root, tag, sha, repository):
    repo = f"repos/{repository}"
    checked_tag(repo, tag, sha)
    files = inventory(root, tag, sha)
    matches = [r for r in pages(f"{repo}/releases?per_page=100") if r["tag_name"] == tag]
    if not matches:
        release = api(
            f"{repo}/releases",
            "-X",
            "POST",
            "-f",
            f"tag_name={tag}",
            "-F",
            "draft=true",
            "-F",
            "generate_release_notes=true",
        )
    else:
        (release,) = matches
    endpoint = f"{repo}/releases/{release['id']}"

    def list_assets():
        return {a["name"]: a for a in pages(f"{endpoint}/assets?per_page=100")}

    def matches_file(asset, path, expected):
        return (
            asset["state"] == "uploaded"
            and asset["size"] == path.stat().st_size
            and remote_hash(asset["url"]) == expected
        )

    if release["draft"]:
        assets = list_assets()
        for name, (path, expected) in files.items():
            existing = assets.get(name)
            if existing and matches_file(existing, path, expected):
                continue
            if not api(endpoint)["draft"]:
                raise RuntimeError("release was published during upload")
            if existing:
                api(existing["url"], "-X", "DELETE")
            subprocess.run(
                ["gh", "release", "upload", tag, str(path), "--repo", repository], check=True
            )

    assets = list_assets()
    if set(files) != set(assets):
        raise ValueError(f"release asset set differs: {set(files) ^ set(assets)}")
    for name, (path, expected) in files.items():
        if not matches_file(assets[name], path, expected):
            raise ValueError(f"uploaded bytes differ for {name}")
    if not api(endpoint)["draft"]:
        verify_public_release(repository, tag, files)
        return
    checked_tag(repo, tag, sha)
    latest = api(f"{repo}/releases/latest")["tag_name"]
    if stable(latest) >= stable(tag):
        print(f"{tag} retained as draft, latest is {latest}")
        return
    api(
        endpoint,
        "-X",
        "PATCH",
        "-F",
        "draft=false",
        "-F",
        "prerelease=false",
        "-f",
        "make_latest=true",
    )
    verify_public_release(repository, tag, files)


if __name__ == "__main__":
    publish(
        Path(sys.argv[1]),
        os.environ["GITHUB_REF_NAME"],
        os.environ["GITHUB_SHA"],
        os.environ["GITHUB_REPOSITORY"],
    )
