"""Check the exact fork Linux AppImage on Ubuntu 24.04 and bind its ABI evidence."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import tempfile

import yaml


RECORD = "linux-appimage-abi.json"
CHANNEL = "latest-linux.yml"
GLIBC = "glibc 2.39"
IMAGE_OS = "ubuntu24"
LOADER = "/lib64/ld-linux-x86-64.so.2"
LIBSTDCXX = "/usr/lib/x86_64-linux-gnu/libstdc++.so.6"
LIBC = "/usr/lib/x86_64-linux-gnu/libc.so.6"
ASAR_EXTRACT = Path(__file__).with_name("extract-release-asar.cjs")
NAMESPACES = ("GLIBC_", "GLIBCXX_", "CXXABI_")
REQUIRED_ELF = {
    "resources/app.asar.unpacked/resources/bin/multica",
    "resources/app.asar.unpacked/node_modules/node-pty/build/Release/pty.node",
    "resources/app.asar.unpacked/resources/voice-engine/sherpa-onnx.node",
}


def command(*args, cwd=None, trace_elf=False):
    env = {key: value for key, value in os.environ.items() if not key.startswith("LD_")}
    env["LC_ALL"] = "C"
    if trace_elf:
        env["LD_TRACE_LOADED_OBJECTS"] = "1"
    result = subprocess.run(args, cwd=cwd, env=env, text=True, capture_output=True, timeout=180)
    if result.returncode or result.stderr:
        raise ValueError(f"{args[0]} failed for {args[-1]}: {result.stderr.strip() or result.stdout.strip()}")
    return result.stdout


def versions(text, section):
    match = re.search(rf"^Version {section} section[^\n]*\n((?: +[^\n]*\n)*)", text, re.M)
    return set(re.findall(r"\bName: (\S+)", match[1])) if match else set()


def elf_versions(path, available, *, bundled_libs=None, appimage=False):
    info = command("readelf", "-W", "-h", "-l", "-d", "-V", str(path))
    if not re.search(r"Class:\s+ELF64\b", info) or not re.search(
        r"Machine:\s+Advanced Micro Devices X86-64\b", info
    ):
        raise ValueError(f"{path}: expected Linux x86-64 ELF")
    needed = versions(info, "needs")
    if "(VERNEED)" in info and not needed:
        raise ValueError(f"{path}: missing ELF version needs output")
    checked = {name for name in needed if name.startswith(NAMESPACES)}
    for name in sorted(checked):
        glibc_version = re.fullmatch(r"GLIBC_(\d+(?:\.\d+)+)", name)
        if glibc_version and tuple(map(int, glibc_version[1].split("."))) > (2, 39):
            raise ValueError(f"{path}: {name} exceeds Ubuntu 24.04 glibc 2.39")
        if name == "GLIBC_PRIVATE" or name not in available:
            raise ValueError(f"{path}: {name} unavailable in the Ubuntu 24.04 baseline libraries")
    interp = re.search(r"Requesting program interpreter: ([^]]+)", info)
    if interp and interp[1] != LOADER:
        raise ValueError(f"{path}: unexpected program interpreter {interp[1]}")
    if interp or "(NEEDED)" in info:
        if appimage and interp:
            # The type 2 magic occupies EI_ABIVERSION. The kernel can load
            # this header, while asking ld.so to open it directly rejects it.
            # Trace dependencies through the runtime's declared interpreter.
            resolved = command(str(path), cwd=path.parent, trace_elf=True)
        else:
            # AppRun prepends the AppDir's usr/lib to the runtime search path.
            search = ("--library-path", str(bundled_libs)) if bundled_libs else ()
            resolved = command(LOADER, *search, "--list", str(path), cwd=path.parent)
        if not resolved.strip() or re.search(r"\bnot found\b", resolved, re.I):
            raise ValueError(f"{path}: unresolved dynamic dependencies: {resolved.strip()}")
    return sorted(checked)


def inspect_tree(root, available, bundled_libs, prefix=""):
    objects = {}
    for path in sorted(root.rglob("*")):
        if path.is_symlink() or not path.is_file():
            continue
        relative = prefix + path.relative_to(root).as_posix()
        with path.open("rb") as stream:
            is_elf = stream.read(4) == b"\x7fELF"
        if is_elf:
            objects[relative] = elf_versions(path, available, bundled_libs=bundled_libs)
        elif path.suffix == ".asar":
            with tempfile.TemporaryDirectory(prefix="asar-", dir=root.parent) as extracted:
                command("node", str(ASAR_EXTRACT), str(path), extracted)
                objects.update(inspect_tree(Path(extracted), available, bundled_libs, relative + "!/"))
    return objects


def hash_bytes(path):
    sha256 = hashlib.sha256()
    sha512 = hashlib.sha512()
    size = 0
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            sha256.update(chunk)
            sha512.update(chunk)
            size += len(chunk)
    if not size:
        raise ValueError(f"{path} is empty")
    return dict(name=path.name, size=size, sha256=sha256.hexdigest(),
                sha512=base64.b64encode(sha512.digest()).decode())


def describe(folder, revision, version=None):
    (image,) = folder.glob("*.AppImage")
    if (folder / "build-revision").read_text().strip() != revision:
        raise ValueError("Linux AppImage build revision mismatch")
    image_info = hash_bytes(image)
    channel = folder / CHANNEL
    metadata = yaml.safe_load(channel.read_bytes())
    actual_version = metadata["version"]
    if not isinstance(actual_version, str) or not actual_version or (version and actual_version != version):
        raise ValueError("Linux AppImage channel version mismatch")
    if image.name != f"multica-desktop-{actual_version}-linux-x86_64.AppImage":
        raise ValueError("Linux AppImage filename does not match channel version")
    (entry,) = [file for file in metadata["files"] if file["url"] == image.name]
    if (entry["size"] != image_info["size"] or entry["sha512"] != image_info["sha512"]
            or metadata["path"] != image.name or metadata["sha512"] != image_info["sha512"]):
        raise ValueError("Linux AppImage bytes do not match the channel")
    return dict(revision=revision, version=actual_version, appimage=image_info, channel=hash_bytes(channel))


def verify_record(folder, revision, version=None):
    record = json.loads((folder / RECORD).read_text())
    current = describe(folder, revision, version)
    if any(record.get(key) != value for key, value in current.items()):
        raise ValueError("Linux ABI evidence does not match the artifact bytes")
    objects = record.get("elf")
    if (
        record.get("schema") != 1
        or record.get("glibc") != GLIBC
        or record.get("image_os") != IMAGE_OS
        or not re.fullmatch(r"[0-9]+(?:\.[0-9]+)+", record.get("image_version", ""))
        or not isinstance(objects, dict)
        or not (REQUIRED_ELF | {"<AppImage runtime>", "multica-desktop"}) <= objects.keys()
        or record.get("runtime_smoke") != "pass"
    ):
        raise ValueError("incomplete Linux ABI or runtime evidence")
    return record


def scan(folder, revision):
    os_release = platform.freedesktop_os_release()
    glibc = command("getconf", "GNU_LIBC_VERSION").strip()
    image_os = os.environ["ImageOS"]
    image_version = os.environ["ImageVersion"]
    print(f"Runner image {image_os} {image_version}, {glibc}", flush=True)
    if (
        (os_release["ID"], os_release["VERSION_ID"], platform.machine(), glibc)
        != ("ubuntu", "24.04", "x86_64", GLIBC)
        or image_os != IMAGE_OS
        or not re.fullmatch(r"[0-9]+(?:\.[0-9]+)+", image_version)
    ):
        raise ValueError("ABI scan requires the Ubuntu 24.04 x86-64 runner with glibc 2.39")
    before = describe(folder, revision)
    image = folder / before["appimage"]["name"]
    available = versions(command("readelf", "-W", "-V", LIBC), "definition")
    available |= versions(command("readelf", "-W", "-V", LIBSTDCXX), "definition")
    if "GLIBC_2.39" not in available or not {"GLIBCXX_3.4", "CXXABI_1.3"} <= available:
        raise ValueError("cannot read the Ubuntu baseline library versions")
    objects = {"<AppImage runtime>": elf_versions(image, available, appimage=True)}
    with tempfile.TemporaryDirectory(prefix="appimage-abi-") as scratch:
        command(str(image), "--appimage-extract", cwd=scratch)
        appdir = Path(scratch) / "squashfs-root"
        objects.update(inspect_tree(appdir, available, appdir / "usr/lib"))
    if not (REQUIRED_ELF | {"multica-desktop"}) <= objects.keys():
        raise ValueError("missing expected Linux ELF files in the AppImage")
    print(f"Resolved ABI and dependencies for {len(objects)} ELF files", flush=True)
    print(json.dumps(before, sort_keys=True), flush=True)
    subprocess.run(
        ["xvfb-run", "-a", "bash", str(Path(__file__).with_name("smoke-linux-appimage.sh")), str(image)],
        check=True, timeout=180,
    )
    if describe(folder, revision) != before:
        raise ValueError("Linux AppImage or update manifest changed during verification")
    record = dict(schema=1, **before, elf=objects, glibc=glibc, image_os=image_os,
                  image_version=image_version, runtime_smoke="pass")
    with (folder / RECORD).open("x") as stream:
        json.dump(record, stream, indent=2, sort_keys=True)
    print("Linux AppImage ABI and runtime verification passed")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["scan", "verify"])
    parser.add_argument("folder", type=Path)
    parser.add_argument("revision")
    args = parser.parse_args()
    if args.action == "scan":
        scan(args.folder.resolve(), args.revision)
    else:
        verify_record(args.folder.resolve(), args.revision)
