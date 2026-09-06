import hashlib
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile

import release


class ReleaseTests(unittest.TestCase):
    def test_version_rejects_unsafe_and_malformed_tags(self):
        self.assertEqual(release.version("v1.0.0-rc.1"), "1.0.0-rc.1")
        self.assertEqual(release.version("v1.2.3"), "1.2.3")
        for tag in ("main", "v1.2", "v01.2.3", "v1.2.3-01", "v1.2.3/x", "v1.2.3;echo bad"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.version(tag)

    def test_archives_contain_executable_readme_and_dependency_notices(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            readme = root / "README.md"
            readme.write_text("ASH usage", encoding="utf-8")
            notices = root / "notices"
            notices.mkdir()
            (notices / "Dependency-LICENSE").write_text("Dependency license", encoding="utf-8")
            for target in release.TARGETS:
                with self.subTest(target=target):
                    binary = root / ("ash.exe" if target == "windows-amd64" else "ash")
                    binary.write_bytes(b"executable")
                    archive = release.package("v1.0.0-rc.1", target, binary, readme, notices, root / "dist")
                    if target == "windows-amd64":
                        self.assertEqual(archive.suffix, ".zip")
                        with zipfile.ZipFile(archive) as handle:
                            self.assertEqual(handle.read("ash.exe"), b"executable")
                            self.assertIn("licenses/Dependency-LICENSE", handle.namelist())
                            self.assertIn("README.md", handle.namelist())
                    else:
                        with tarfile.open(archive) as handle:
                            self.assertEqual(handle.extractfile("ash").read(), b"executable")
                            self.assertEqual(handle.getmember("ash").mode, 0o755)
                            self.assertIn("licenses/Dependency-LICENSE", handle.getnames())
                            self.assertIn("README.md", handle.getnames())
            sums = release.checksums("v1.0.0-rc.1", root / "dist")
            lines = sums.read_text().splitlines()
            self.assertEqual(len(lines), 4)
            for line in lines:
                digest, name = line.split("  ")
                self.assertEqual(digest, hashlib.sha256((root / "dist" / name).read_bytes()).hexdigest())
            archive.unlink()
            with self.assertRaises(ValueError):
                release.checksums("v1.0.0-rc.1", root / "dist")


if __name__ == "__main__":
    unittest.main()
