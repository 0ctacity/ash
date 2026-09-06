import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


@unittest.skipIf(os.name == 'nt', 'POSIX installer runs on Unix')
class InstallTests(unittest.TestCase):
    def run_installer(self, system='Linux', arch='x86_64', corrupt=False, fail=False, version=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            mocks = root / 'mocks'
            mocks.mkdir()
            payload = b'#!/bin/sh\necho ash-test\n'
            archive = root / 'archive.tar.gz'
            with tarfile.open(archive, 'w:gz') as handle:
                info = tarfile.TarInfo('ash')
                info.size = len(payload)
                info.mode = 0o755
                handle.addfile(info, io.BytesIO(payload))
            digest = '0' * 64 if corrupt else hashlib.sha256(archive.read_bytes()).hexdigest()
            (mocks / 'uname').write_text(f'#!/bin/sh\ncase "$1" in -s) echo {system};; -m) echo {arch};; esac\n')
            (mocks / 'curl').write_text('''#!/bin/sh
set -eu
out=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift;;
    -w|--proto|--tlsv1.2) if [ "$1" != --tlsv1.2 ]; then shift; fi;;
    https:*) url=$1;;
  esac
  shift
done
[ "$FAIL_DOWNLOAD" = 0 ] || exit 22
case "$url" in
  */releases/latest) printf 'https://github.com/0ctacity/ash/releases/tag/v1.2.3';;
  */SHA256SUMS.txt) printf '%s  %s\n' "$DIGEST" "$ASSET" > "$out";;
  */"$ASSET") cp "$ARCHIVE" "$out";;
  *) exit 23;;
esac
''')
            for mock in mocks.iterdir():
                mock.chmod(0o755)
            target = ('macos' if system == 'Darwin' else 'linux') + '-' + ('arm64' if arch in ('arm64', 'aarch64') else 'amd64')
            destination = root / 'install with spaces'
            destination.mkdir()
            (destination / 'ash').write_bytes(b'previous binary')
            env = dict(os.environ, PATH=str(mocks) + os.pathsep + os.environ['PATH'],
                       ASH_INSTALL_DIR=str(destination), ARCHIVE=str(archive), DIGEST=digest,
                       ASSET=f'ash-v1.2.3-{target}.tar.gz', FAIL_DOWNLOAD=str(int(fail)))
            env.pop('ASH_VERSION', None)
            if version:
                env['ASH_VERSION'] = version
            result = subprocess.run(['sh', str(Path(__file__).resolve().parents[1] / 'install.sh')],
                                    env=env, capture_output=True, text=True)
            return result, (destination / 'ash').read_bytes(), payload

    def test_supported_platforms_and_latest_release(self):
        for system, arch in [('Linux', 'x86_64'), ('Linux', 'aarch64'), ('Darwin', 'arm64')]:
            with self.subTest(system=system, arch=arch):
                result, installed, payload = self.run_installer(system, arch)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(installed, payload)

    def test_pinned_version(self):
        result, installed, payload = self.run_installer(version='v1.2.3')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(installed, payload)

    def test_failures_preserve_existing_installation(self):
        for options in [dict(corrupt=True), dict(fail=True), dict(system='Darwin', arch='x86_64'), dict(version='../bad')]:
            with self.subTest(options=options):
                result, installed, _ = self.run_installer(**options)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(installed, b'previous binary')
                self.assertNotIn('cannot open', result.stderr)
