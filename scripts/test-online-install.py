"""Linux shell smoke: no network, accounts, services, or real installation."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('build_release', ROOT / 'scripts/build-release.py')
build_release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build_release)


class OnlineInstallTest(unittest.TestCase):
    def test_bootstrap_trust_and_arguments(self):
        with tempfile.TemporaryDirectory(prefix='filepanel installer ') as tmp:
            root = Path(tmp)
            mock = root / 'bin'
            mock.mkdir()
            dest = root / 'release'
            dest.mkdir()
            payload = b'#!/bin/sh\nprintf "%s\\n" "$@" >> "$TEST_CALLS"\n'
            digest = hashlib.sha256(payload).hexdigest()
            manifest = {'manifest': {'artifacts': {
                'linux-' + arch: {'sha256': digest} for arch in ['amd64', 'arm64']}}}
            (dest / 'release.json').write_text(json.dumps(manifest))
            (dest / 'trusted.pub').write_text('cHVibGljLWtleQ==')
            for arch in ['amd64', 'arm64']:
                (dest / ('filepanel-linux-' + arch)).write_bytes(payload)
            build_release.online_installer(dest, '0.2.0')

            def command(name, content):
                path = mock / name
                path.write_text('#!/bin/sh\nset -eu\n' + content + '\n')
                path.chmod(0o755)

            command('uname', 'case "$1" in -s) echo Linux;; -m) echo "${TEST_ARCH:-x86_64}";; esac')
            command('id', 'echo "${TEST_UID:-0}"')
            command('mktemp', 'mkdir "$TEST_WORK"\nprintf "%s\\n" "$TEST_WORK"')
            command('curl', '''
out=
while [ "$#" -gt 0 ]; do
    case "$1" in --output) out=$2; shift 2;; *) url=$1; shift;; esac
done
[ "${TEST_DOWNLOAD_FAIL:-0}" = 0 ] || exit 22
cp "$TEST_RELEASE/${url##*/}" "$out"
if [ "${TEST_CORRUPT:-}" = "${url##*/}" ]; then printf x >> "$out"; fi
''')
            calls = root / 'calls'
            work = root / 'download'
            env = dict(os.environ, PATH=str(mock) + ':' + os.environ['PATH'],
                       TEST_RELEASE=str(dest), TEST_WORK=str(work), TEST_CALLS=str(calls))
            script = str(dest / 'install-online.sh')
            for arch in ['x86_64', 'aarch64']:
                result = subprocess.run(['sh', script, '--instance', 'trial', '--listen', '127.0.0.1:18999'],
                                        env=dict(env, TEST_ARCH=arch), capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                args = calls.read_text().splitlines()
                self.assertEqual(args[0], 'install')
                self.assertIn('127.0.0.1:18999', args)
                self.assertIn('cHVibGljLWtleQ==', args)
                self.assertIn('init-token', args)
                self.assertFalse(work.exists(), 'temporary download directory leaked')
                calls.unlink()
            for extra, options in [
                ({'TEST_CORRUPT': 'release.json'}, []),
                ({'TEST_CORRUPT': 'filepanel-linux-amd64'}, []),
                ({'TEST_DOWNLOAD_FAIL': '1'}, []),
                ({'TEST_ARCH': 'riscv64'}, []),
                ({'TEST_UID': '1000'}, []),
                ({'FILEPANEL_RELEASE_BASE': 'http://unsafe.invalid'}, []),
                ({}, ['--release', '/untrusted.json']),
                ({}, ['--instance', '../other']),
                ({}, ['--listen']),
            ]:
                result = subprocess.run(['sh', script, *options], env=dict(env, **extra), capture_output=True)
                self.assertNotEqual(result.returncode, 0, (extra, options))
                self.assertFalse(calls.exists(), 'unverified program executed')
                self.assertFalse(work.exists(), 'failed download directory leaked')


if __name__ == '__main__':
    unittest.main()
