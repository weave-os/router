"""Offline coverage for first-run jq acquisition without a package manager."""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

INSTALLER = Path(__file__).resolve().parents[1] / "install.sh"


class JqBootstrapTest(unittest.TestCase):
    def run_bootstrap(self, system: str, architecture: str, checksum: str, corrupt: bool = False, checksum_program: str = "sha256sum", fail_chmod: bool = False) -> tuple[subprocess.CompletedProcess[str], Path]:
        workspace = tempfile.TemporaryDirectory()
        self.addCleanup(workspace.cleanup)
        home = Path(workspace.name)
        binaries = home / "tools"
        binaries.mkdir()
        for name in ("mkdir", "mktemp", "awk", "chmod", "rm"):
            executable = subprocess.check_output(["/usr/bin/which", name], text=True).strip()
            (binaries / name).symlink_to(executable)
        if fail_chmod:
            (binaries / "chmod").unlink()
            chmod = binaries / "chmod"
            chmod.write_text("#!/bin/sh\nexit 1\n")
            chmod.chmod(0o755)
        shasum_path = subprocess.check_output(["/usr/bin/which", "shasum"], text=True).strip()
        scripts = {
            "mv": '#!/bin/sh\n/bin/mv "$@"\nfor destination; do :; done\ncase "$destination" in *.exe) /bin/ln -s "$destination" "${destination%.exe}";; esac\n',
            "uname": f'#!/bin/sh\ncase "$1" in -s) echo "{system}";; -m) echo "{architecture}";; esac\n',
            "curl": '#!/bin/sh\nprintf "%s\\n" "$@" > "$HOME/download-args"\nwhile [ "$1" != "-o" ]; do shift; done\nprintf \'#!/bin/sh\\necho jq-' + ("corrupt" if corrupt else "1.8.1") + '\\n\' > "$2"\n',
            checksum_program: f'#!/bin/sh\nprintf "%s\\n" "$@" > "$HOME/{checksum_program}-args"\n' + (f'exec "{shasum_path}" -a 256 "$@"\n' if checksum_program == "sha256sum" else f'exec "{shasum_path}" "$@"\n'),
        }
        for name, script in scripts.items():
            executable = binaries / name
            executable.write_text(script)
            executable.chmod(0o755)
        source = INSTALLER.read_text()
        downloaded_binary = b"#!/bin/sh\necho jq-1.8.1\n"
        mock_checksum = hashlib.sha256(downloaded_binary).hexdigest()
        self.assertIn(f"checksum={checksum}", source)
        source = source.replace(f"checksum={checksum}", f"checksum={mock_checksum}", 1)
        cleanup_start = source.index("_spin_cleanup() {")
        cleanup_end = source.index("\ntrap _spin_cleanup", cleanup_start)
        start = source.index("ensure_jq() {")
        end = source.index("\n#", source.index("require_cmd() {", start))
        shell = 'set -euo pipefail\nexport PATH="$PATH:$HOME/.weave/bin"\nerr() { echo "$*" >&2; }\ninfo() { :; }\ntty_out=false\nspin_pid=""\nspin_log=""\n' + source[cleanup_start:cleanup_end] + '\ntrap _spin_cleanup EXIT\n' + source[start:end] + '\nensure_jq\njq --version\n'
        completed = subprocess.run(["/bin/bash", "-c", shell], env={**os.environ, "HOME": str(home), "PATH": str(binaries)}, text=True, capture_output=True)
        return completed, home

    def test_supported_platforms_without_jq(self) -> None:
        platforms = [
            ("Darwin", "arm64", "jq-macos-arm64", "a9fe3ea2f86dfc72f6728417521ec9067b343277152b114f4e98d8cb0e263603"),
            ("Darwin", "x86_64", "jq-macos-amd64", "e80dbe0d2a2597e3c11c404f03337b981d74b4a8504b70586c354b7697a7c27f"),
            ("Linux", "x86_64", "jq-linux-amd64", "020468de7539ce70ef1bceaf7cde2e8c4f2ca6c3afb84642aabc5c97d9fc2a0d"),
            ("Linux", "aarch64", "jq-linux-arm64", "6bc62f25981328edd3cfcfe6fe51b073f2d7e7710d7ef7fcdac28d4e384fc3d4"),
            ("MINGW64_NT-10.0", "x86_64", "jq-windows-amd64.exe", "23cb60a1354eed6bcc8d9b9735e8c7b388cd1fdcb75726b93bc299ef22dd9334"),
        ]
        for system, architecture, asset, checksum in platforms:
            with self.subTest(system=system, architecture=architecture):
                completed, home = self.run_bootstrap(system, architecture, checksum)
                self.assertEqual(completed.returncode, 0, completed.stderr)
                self.assertIn("jq-1.8.1", completed.stdout)
                self.assertIn(f"/jq-1.8.1/{asset}", (home / "download-args").read_text())
                self.assertTrue((home / ".weave/bin" / ("jq.exe" if asset.endswith(".exe") else "jq")).is_file())

    def test_corrupt_download_is_not_installed(self) -> None:
        checksum = "a9fe3ea2f86dfc72f6728417521ec9067b343277152b114f4e98d8cb0e263603"
        completed, home = self.run_bootstrap("Darwin", "arm64", checksum, corrupt=True)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("checksum verification", completed.stderr)
        self.assertEqual(list((home / ".weave/bin").iterdir()), [])

    def test_shasum_fallback_verifies_download(self) -> None:
        checksum = "a9fe3ea2f86dfc72f6728417521ec9067b343277152b114f4e98d8cb0e263603"
        completed, home = self.run_bootstrap("Darwin", "arm64", checksum, checksum_program="shasum")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual((home / "shasum-args").read_text().split()[:2], ["-a", "256"])
        self.assertTrue((home / ".weave/bin/jq").is_file())

    def test_shasum_fallback_rejects_mismatched_download(self) -> None:
        checksum = "a9fe3ea2f86dfc72f6728417521ec9067b343277152b114f4e98d8cb0e263603"
        completed, home = self.run_bootstrap("Darwin", "arm64", checksum, corrupt=True, checksum_program="shasum")
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("checksum verification", completed.stderr)
        self.assertEqual(list((home / ".weave/bin").iterdir()), [])

    def test_failed_install_cleans_temporary_download(self) -> None:
        checksum = "a9fe3ea2f86dfc72f6728417521ec9067b343277152b114f4e98d8cb0e263603"
        completed, home = self.run_bootstrap("Darwin", "arm64", checksum, fail_chmod=True)
        self.assertNotEqual(completed.returncode, 0)
        self.assertEqual(list((home / ".weave/bin").iterdir()), [])


if __name__ == "__main__":
    unittest.main()
