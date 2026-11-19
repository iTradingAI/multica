"""Exercise fork release decisions without publishing a tag or registry alias."""

import base64
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import yaml


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

    def test_each_channel_matches_the_retained_bytes(self):
        self.assertEqual(len(desktop.inventory(self.root, "v1.2.3", "test-revision")), 17)

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

    def test_existing_release_retries_are_read_only(self):
        files = desktop.inventory(self.root, "v1.2.3", "test-revision")
        assets = [
            dict(name=name, url=name, state="uploaded", size=path.stat().st_size)
            for name, (path, _) in files.items()
        ]
        release = dict(id=12, tag_name="v1.2.3", draft=False)
        with (
            patch.object(desktop, "checked_tag"),
            patch.object(
                desktop,
                "pages",
                side_effect=lambda path: assets if "/assets?" in path else [release],
            ),
            patch.object(desktop, "api", return_value=release) as api,
            patch.object(desktop, "remote_hash", side_effect=lambda name: files[name][1]),
            patch.object(desktop.subprocess, "run") as upload,
        ):
            desktop.publish(self.root, "v1.2.3", "test-revision", "owner/repo")
            self.assertTrue(all(len(c.args) == 1 for c in api.call_args_list))
            upload.assert_not_called()


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
