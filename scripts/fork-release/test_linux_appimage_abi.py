from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import linux_appimage_abi as abi


HEADER = """  Class:                             ELF64
  Machine:                           Advanced Micro Devices X86-64
  0x0000000000000001 (NEEDED)         Shared library: [libc.so.6]
  0x000000006ffffffe (VERNEED)        0x400
Version needs section '.gnu.version_r' contains 1 entry:
  000000: Version: 1  File: libc.so.6  Cnt: 1
  0x0010:   Name: GLIBC_2.39  Flags: none  Version: 2
"""


class ElfTest(unittest.TestCase):
    def test_checks_imports_not_version_definitions(self):
        output = HEADER + """Version definition section '.gnu.version_d' contains 1 entry:
  0x0000: Rev: 1  Flags: none  Index: 1  Cnt: 1  Name: GLIBC_2.42
"""
        with patch.object(abi, "command", side_effect=[output, "libc.so.6 => /usr/lib/libc.so.6"]):
            self.assertEqual(abi.elf_versions(Path("native.node"), {"GLIBC_2.39"}), ["GLIBC_2.39"])

    def test_rejects_imports_above_the_floor_and_missing_cpp_versions(self):
        for name, diagnostic in (
            ("GLIBC_2.42", "exceeds"),
            ("GLIBCXX_3.4.99", "unavailable"),
            ("CXXABI_1.3.99", "unavailable"),
        ):
            with self.subTest(name=name), patch.object(
                abi, "command", return_value=HEADER.replace("GLIBC_2.39", name)
            ):
                with self.assertRaisesRegex(ValueError, diagnostic):
                    abi.elf_versions(Path("native.node"), {"GLIBC_2.39"})

    def test_rejects_loader_diagnostics_even_on_zero_exit(self):
        with patch.object(abi.subprocess, "run", return_value=subprocess.CompletedProcess(
            [], 0, "libc.so.6 => /usr/lib/libc.so.6", "version 'GLIBC_2.42' not found"
        )):
            with self.assertRaisesRegex(ValueError, "not found"):
                abi.command(abi.LOADER, "--list", "pty.node")
        with patch.object(abi, "command", side_effect=[HEADER, "libexample.so => not found"]):
            with self.assertRaisesRegex(ValueError, "unresolved"):
                abi.elf_versions(Path("native.node"), {"GLIBC_2.39"})

    def test_walks_elf_by_magic_and_opens_asar(self):
        with tempfile.TemporaryDirectory() as scratch:
            root = Path(scratch)
            (root / "module.node").write_bytes(b"MZ")
            (root / "helper").write_bytes(b"\x7fELF")
            (root / "app.asar").write_bytes(b"archive")

            def extract(*args):
                (Path(args[-1]) / "inside.so").write_bytes(b"\x7fELF")

            with patch.object(abi, "command", side_effect=extract), patch.object(
                abi, "elf_versions", return_value=[]
            ):
                self.assertEqual(abi.inspect_tree(root, set()), {"helper": [], "app.asar!/inside.so": []})

    @unittest.skipUnless(Path(abi.LOADER).exists() and shutil.which("gcc") and shutil.which("readelf"),
                         "uses the Linux loader and GNU tools")
    def test_traces_the_original_appimage_runtime_with_type2_magic(self):
        with tempfile.TemporaryDirectory() as scratch:
            root = Path(scratch)
            image = root / "test.AppImage"
            (root / "runtime.c").write_text("int main(void) { return 19; }")
            subprocess.run(["gcc", "-o", image, "runtime.c"], cwd=root, check=True)
            with image.open("r+b") as stream:
                stream.seek(8)
                stream.write(b"AI")
            original = image.read_bytes()
            # A successful dependency trace also proves main() was never called.
            self.assertIn("GLIBC_2.2.5", abi.elf_versions(
                image, {"GLIBC_2.2.5", "GLIBC_2.34"}, appimage=True,
            ))
            self.assertEqual(image.read_bytes(), original)

    @unittest.skipUnless(shutil.which("gcc") and shutil.which("readelf"), "uses GNU binutils")
    def test_real_elf_with_a_242_version_need(self):
        with tempfile.TemporaryDirectory() as scratch:
            root = Path(scratch)
            (root / "provider.c").write_text("void sample(void) {}")
            (root / "consumer.c").write_text("void sample(void); void use(void) { sample(); }")
            (root / "versions").write_text("GLIBC_2.42 { global: sample; };")
            subprocess.run(
                ["gcc", "-shared", "-fPIC", "-Wl,--version-script=versions",
                 "-o", "libprovider.so", "provider.c"], cwd=root, check=True,
            )
            subprocess.run(
                ["gcc", "-shared", "-fPIC", "-o", "pty.node", "consumer.c", "-L.", "-lprovider"],
                cwd=root, check=True,
            )
            with self.assertRaisesRegex(ValueError, "GLIBC_2.42 exceeds"):
                abi.elf_versions(root / "pty.node", set())
