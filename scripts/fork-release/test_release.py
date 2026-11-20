"""Exercise fork release decisions without publishing a tag or registry alias."""

import base64
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import yaml

import linux_appimage_abi as abi


def load(filename):
    spec = importlib.util.spec_from_file_location(
        filename, Path(__file__).with_name(filename + ".py")
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


desktop = load("publish-desktop")
container = load("container-pair")


class DesktopReleaseTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for target, (channel, platform, _) in desktop.CHANNELS.items():
            directory = self.root / target
            directory.mkdir()
            (directory / "build-revision").write_text("test-revision")
            extensions = {
                "linux": ["-x86_64.AppImage", "-amd64.deb", "-x86_64.rpm"],
                "windows-x64": [".exe", ".exe.blockmap"],
            }.get(platform, [".zip", ".dmg", ".zip.blockmap", ".dmg.blockmap"])
            entries = []
            for ext in extensions:
                name = f"multica-desktop-1.2.3-{platform}{ext}"
                payload = name.encode()
                (directory / name).write_bytes(payload)
                if not ext.endswith("blockmap"):
                    entries.append(
                        dict(
                            url=name,
                            size=len(payload),
                            sha512=base64.b64encode(hashlib.sha512(payload).digest()).decode(),
                        )
                    )
            metadata = dict(
                version="1.2.3", files=entries, path=entries[0]["url"], sha512=entries[0]["sha512"]
            )
            if platform.startswith("mac"):
                metadata["minimumSystemVersion"] = "22.0.0"
            (directory / channel).write_text(yaml.safe_dump(metadata))
            if platform == "linux":
                record = dict(
                    schema=1, **abi.describe(directory, "test-revision"),
                    glibc=abi.GLIBC, image_os=abi.IMAGE_OS, image_version="20261120.1",
                    elf={name: [] for name in abi.REQUIRED_ELF | {"<AppImage runtime>", "multica-desktop"}},
                    runtime_smoke="pass",
                )
                (directory / abi.RECORD).write_text(json.dumps(record))

    def test_each_channel_matches_the_retained_bytes(self):
        self.assertEqual(len(desktop.inventory(self.root, "v1.2.3", "test-revision")), 17)

    def test_linux_gate_blocks_missing_skipped_or_mutated_artifacts(self):
        linux = self.root / "desktop-linux-x64"
        record = linux / abi.RECORD
        original = record.read_bytes()
        record.unlink()
        with self.assertRaises(FileNotFoundError):
            desktop.inventory(self.root, "v1.2.3", "test-revision")
        record.write_text(json.dumps(dict(json.loads(original), runtime_smoke="skipped")))
        with self.assertRaisesRegex(ValueError, "incomplete Linux"):
            desktop.inventory(self.root, "v1.2.3", "test-revision")
        record.write_bytes(original)
        channel = linux / abi.CHANNEL
        channel.write_bytes(channel.read_bytes() + bytes([10]))
        with self.assertRaisesRegex(ValueError, "artifact bytes"):
            desktop.inventory(self.root, "v1.2.3", "test-revision")
        next(linux.glob("*.AppImage")).write_bytes(b"changed")
        with self.assertRaisesRegex(ValueError, "do not match the channel"):
            desktop.inventory(self.root, "v1.2.3", "test-revision")

    def test_missing_asset_stops_before_draft_mutation(self):
        next(self.root.rglob("*.dmg")).unlink()
        with patch.object(desktop, "checked_tag"), patch.object(desktop, "api") as write:
            with self.assertRaises(ValueError):
                desktop.publish(self.root, "v1.2.3", "test-revision", "owner/repo")
            write.assert_not_called()

    def test_corrupt_or_cross_revision_build_is_rejected(self):
        next(self.root.rglob("*.exe")).write_bytes(b"wrong")
        with self.assertRaises(ValueError):
            desktop.inventory(self.root, "v1.2.3", "test-revision")
        with self.assertRaises(ValueError):
            desktop.inventory(self.root, "v1.2.3", "different")

    def test_published_retry_repeats_failed_public_checks_without_mutating_release(self):
        tag = "v1.2.3"
        files = desktop.inventory(self.root, tag, "test-revision")
        assets = [
            dict(id=idx, name=name, url=name, state="uploaded", size=path.stat().st_size)
            for idx, (name, (path, _)) in enumerate(files.items(), 1)
        ]
        release = dict(id=12, tag_name=tag, draft=True)
        public_latest = "v1.2.2"
        asset_unavailable = None
        latest_timeout = True
        reads = []

        def release_api(path, *args):
            nonlocal public_latest
            if args:
                self.assertEqual(args[:2], ("-X", "PATCH"))
                self.assertTrue(release["draft"])
                release["draft"] = False
                public_latest = tag
            if path.endswith("/latest"):
                return {"tag_name": public_latest}
            return release

        def anonymous_request(request, **kwargs):
            url = request if isinstance(request, str) else request.full_url
            reads.append(url)
            if url.endswith("/latest"):
                if latest_timeout:
                    raise TimeoutError("anonymous latest timed out")
                return io.BytesIO(json.dumps({"tag_name": public_latest}).encode())
            self.assertFalse(request.has_header("Authorization"))
            self.assertEqual(request.get_method(), "HEAD")
            response = io.BytesIO()
            response.status = 503 if url.rsplit("/", 1)[1] == asset_unavailable else 200
            return response

        with (
            patch.object(desktop, "checked_tag"),
            patch.object(
                desktop,
                "pages",
                side_effect=lambda path: assets if "/assets?" in path else [release],
            ),
            patch.object(desktop, "api", side_effect=release_api) as api,
            patch.object(desktop, "remote_hash", side_effect=lambda name: files[name][1]) as hashes,
            patch.object(desktop.subprocess, "run") as upload,
            patch.object(desktop.urllib.request, "urlopen", side_effect=anonymous_request),
        ):
            with self.assertRaisesRegex(TimeoutError, "anonymous latest timed out"):
                desktop.publish(self.root, tag, "test-revision", "owner/repo")
            self.assertFalse(release["draft"])
            api.reset_mock()

            with self.assertRaisesRegex(TimeoutError, "anonymous latest timed out"):
                desktop.publish(self.root, tag, "test-revision", "owner/repo")

            latest_timeout = False
            asset_unavailable = "latest-x64-mac.yml"
            with self.assertRaisesRegex(RuntimeError, "latest-x64-mac.yml"):
                desktop.publish(self.root, tag, "test-revision", "owner/repo")

            asset_unavailable = None
            reads.clear()
            hashes.reset_mock()
            desktop.publish(self.root, tag, "test-revision", "owner/repo")
            self.assertEqual(reads[0], "https://api.github.com/repos/owner/repo/releases/latest")
            self.assertCountEqual(
                reads[1:],
                [f"https://github.com/owner/repo/releases/download/{tag}/{name}" for name in files],
            )
            self.assertCountEqual([c.args[0] for c in hashes.call_args_list], files)

            reads.clear()
            public_latest = "v1.2.2"
            with self.assertRaisesRegex(RuntimeError, "anonymous latest lookup differs"):
                desktop.publish(self.root, tag, "test-revision", "owner/repo")

            reads.clear()
            public_latest = "v1.2.4"
            desktop.publish(self.root, tag, "test-revision", "owner/repo")
            self.assertEqual(reads, ["https://api.github.com/repos/owner/repo/releases/latest"])
            self.assertTrue(all(len(c.args) == 1 for c in api.call_args_list))
            upload.assert_not_called()


class ContainerPromotionTest(unittest.TestCase):
    tag = "v1.2.3"
    revision = "a" * 40
    repository = "owner/multica"

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.sealed = {
            role: self.image(role, self.tag, self.revision, n)
            for role, n in [("backend", 1), ("web", 2)]
        }
        self.registry = {}
        for role, item in self.sealed.items():
            repo = item["repository"]
            previous = self.image(role, "v1.2.2", "b" * 40, 3)
            self.registry[f"{repo}:latest"] = previous
            self.registry[f"{repo}@{item['digest']}"] = item
            directory = self.root / role
            directory.mkdir()
            for digit in ("6", "7"):
                (directory / (digit * 64)).touch()

    def image(self, role, tag, revision, n):
        return dict(
            repository=f"ghcr.io/{self.repository}-{role}",
            tag=tag,
            revision=revision,
            digest=f"sha256:{str(n) * 64}",
            platforms={arch: "sha256:" + digit * 64 for arch, digit in [("amd64", "6"), ("arm64", "7")]},
        )

    def run_command(self, *args):
        ref = args[-1]
        if args[:2] == ("skopeo", "list-tags"):
            repo = ref.removeprefix("docker://")
            tags = [key.removeprefix(repo + ":") for key in self.registry if key.startswith(repo + ":")]
            return json.dumps({"Tags": tags}).encode()
        if ref.endswith(f"/commits/{self.tag}"):
            return json.dumps({"sha": self.revision}).encode()
        self.assertTrue(ref.endswith("/tags?per_page=100"))
        return json.dumps([[{"name": "v1.2.2"}, {"name": self.tag}]]).encode()

    def retain(self):
        container.retain(self.tag, self.revision, "100", "2", "owner", self.root)

    def test_missing_manifest_stops_before_any_registry_write_when_an_alias_exists(self):
        for role in ("backend", "web"):
            for alias in (self.tag, "latest"):
                with self.subTest(role=role, alias=alias):
                    ref = f"{self.sealed[role]['repository']}:{alias}"
                    prior = self.registry.get(ref)
                    self.registry[ref] = self.sealed[role]
                    before = self.registry.copy()
                    try:
                        with (
                            patch.object(container, "inspect", side_effect=self.registry.__getitem__),
                            patch.object(container, "run", side_effect=self.run_command),
                            patch.object(container.subprocess, "run") as write,
                        ):
                            with self.assertRaisesRegex(ValueError, "sealed manifest"):
                                self.retain()
                            write.assert_not_called()
                            self.assertEqual(self.registry, before)
                    finally:
                        if prior is None:
                            del self.registry[ref]
                        else:
                            self.registry[ref] = prior

    def test_unreadable_tag_listing_stops_before_any_registry_write(self):
        with (
            patch.object(container, "run", side_effect=OSError("registry unavailable")),
            patch.object(container.subprocess, "run") as write,
        ):
            with self.assertRaisesRegex(OSError, "registry unavailable"):
                self.retain()
            write.assert_not_called()

    def test_first_seal_keeps_both_digests_when_only_older_aliases_exist(self):
        manifest = self.root / "container-pair.json"

        def stage_index(args, *, check):
            self.assertTrue(check)
            target = args[-3]
            role = "backend" if "multica-backend:" in target else "web"
            self.registry[target] = self.sealed[role]

        with (
            patch.object(container, "inspect", side_effect=self.registry.__getitem__),
            patch.object(container, "run", side_effect=self.run_command),
            patch.object(container.subprocess, "run", side_effect=stage_index),
            patch.object(container, "Path", return_value=manifest),
        ):
            self.retain()
        self.assertEqual(
            json.loads(manifest.read_text()),
            {"run": "100", "attempt": "2", "images": self.sealed},
        )

    def test_manifest_recovery_only_fills_missing_aliases_with_original_digests(self):
        backend = self.sealed["backend"]
        web = self.sealed["web"]
        self.registry[f"{backend['repository']}:{self.tag}"] = backend
        self.registry[f"{backend['repository']}:latest"] = backend
        rebuilt = self.image("web", self.tag, self.revision, 4)
        self.registry[f"{rebuilt['repository']}@{rebuilt['digest']}"] = rebuilt
        manifest = self.root / "container-pair.json"
        manifest.write_text(json.dumps({"run": "100", "attempt": "1", "images": self.sealed}))

        def copy_alias(args, *, check):
            self.assertTrue(check)
            dst, src = args[-2:]
            self.registry[dst] = self.registry[src]

        with (
            patch.object(container, "inspect", side_effect=self.registry.__getitem__),
            patch.object(container, "run", side_effect=self.run_command),
            patch.object(container.subprocess, "run", side_effect=copy_alias) as write,
        ):
            container.promote(manifest, self.tag, self.revision, self.repository)
            self.assertEqual(
                [c.args[0][-2:] for c in write.call_args_list],
                [
                    [f"{web['repository']}:{self.tag}", f"{web['repository']}@{web['digest']}"],
                    [f"{web['repository']}:latest", f"{web['repository']}@{web['digest']}"],
                ],
            )
            write.reset_mock()
            container.promote(manifest, self.tag, self.revision, self.repository)
            write.assert_not_called()


class ConsumerTest(unittest.TestCase):
    def setUp(self):
        self.pair = {
            role: dict(tag="v1.2.3", revision="a" * 40, digest="sha256:" + digit * 64)
            for role, digit in [("backend", "1"), ("web", "2")]
        }

    def pin(self, previous):
        def lookup(ref):
            return self.pair["backend" if "multica-backend:" in ref else "web"]

        with patch.object(container, "inspect", side_effect=lookup):
            return container.consumer_inputs("owner/multica", previous)

    def test_pin_both_aliases_to_the_verified_digests(self):
        pair, override = self.pin(self.pair)
        self.assertEqual(pair, self.pair)
        for role, service in [("web", "frontend"), ("backend", "backend")]:
            self.assertEqual(
                override["services"][service]["image"],
                f"ghcr.io/owner/multica-{role}@{pair[role]['digest']}",
            )

    def test_partial_promotion_and_rollback_both_stop(self):
        previous = {r: dict(tag="v1.2.2", digest="previous") for r in self.pair}
        self.pair["web"].update(tag="v1.2.2", revision="b" * 40, digest="previous")
        with self.assertRaisesRegex(ValueError, "different releases"):
            self.pin(previous)
        previous["backend"]["tag"] = "v1.2.4"
        with self.assertRaisesRegex(ValueError, "regressed"):
            self.pin(previous)
