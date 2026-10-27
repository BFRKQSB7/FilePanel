"""Build Linux static signed local releases. Signing keys stay outside packages.

python scripts/build-release.py --fixture
python scripts/build-release.py --key /private/offline.key --version 0.1.0 --sequence 1
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]


def online_installer(dest, version):
    manifest = json.loads((dest / 'release.json').read_text(encoding='utf-8'))['manifest']
    replacements = {
        '@VERSION@': version,
        '@MANIFEST_SHA256@': hashlib.sha256((dest / 'release.json').read_bytes()).hexdigest(),
        '@UPDATE_KEY@': (dest / 'trusted.pub').read_text(encoding='utf-8').strip(),
        '@AMD64_SHA256@': manifest['artifacts']['linux-amd64']['sha256'],
        '@ARM64_SHA256@': manifest['artifacts']['linux-arm64']['sha256'],
    }
    script = (ROOT / 'scripts/install-online.sh').read_text(encoding='utf-8')
    for token, value in replacements.items():
        script = script.replace(token, value)
    (dest / 'install-online.sh').write_text(script, encoding='utf-8', newline='\n')


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--fixture', action='store_true')
    p.add_argument('--key', type=Path)
    p.add_argument('--version', default='0.6.3')
    p.add_argument('--sequence', type=int, default=16)
    p.add_argument('--output', type=Path, default=ROOT / 'dist/t08-release')
    a = p.parse_args()
    assert a.fixture or a.key, 'provide an existing offline key or --fixture'
    a.output = a.output.resolve()
    a.output.mkdir(parents=True, exist_ok=True)
    source = a.output / 'source.zip'
    names = run(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], capture_output=True).stdout.decode().split('\0')
    with zipfile.ZipFile(source, 'w', zipfile.ZIP_DEFLATED) as z:
        for name in sorted(set(filter(None, names))):
            # Hash every build input; task status and acceptance prose are not
            # compilation inputs and may be finalized after validation.
            if name not in {'go.mod', 'go.sum', 'LICENSE'} and not name.startswith(('cmd/', 'internal/', 'migrations/', 'scripts/')):
                continue
            path = ROOT / name
            assert path.is_file() and not path.is_symlink(), name
            assert name != 'TEST_ENVIRONMENT.md'
            info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(info, path.read_bytes())
    digest = hashlib.sha256(source.read_bytes()).hexdigest()
    key = a.key.resolve() if a.key else a.output / 'fixture-private.key'
    versions = [('v1', '0.1.0', 1), ('v2', '0.1.1', 2), ('bad', '0.1.2', 3)] if a.fixture else [('release', a.version, a.sequence)]
    for label, version, seq in versions:
        dest = a.output / label
        dest.mkdir(exist_ok=True)
        for arch in ['amd64', 'arm64']:
            env = dict(os.environ, GOOS='linux', GOARCH=arch, CGO_ENABLED='0')
            output = dest / ('filepanel-linux-' + arch)
            if label == 'bad':
                with tempfile.TemporaryDirectory() as tmp:
                    src = Path(tmp) / 'main.go'
                    src.write_text('package main\nimport "os"\nfunc main(){os.Exit(1)}\n', encoding='utf-8')
                    run(['go', 'build', '-buildvcs=false', '-trimpath', '-o', str(output), str(src)], env=env)
            else:
                flags = f'-s -w -X filepanel/internal/release.Version={version} -X filepanel/internal/release.Sequence={seq} -X filepanel/internal/release.Source={digest}'
                run(['go', 'build', '-buildvcs=false', '-trimpath', '-ldflags=' + flags, '-o', str(output), './cmd/filepanel'], env=env)
        cmd = ['go', 'run', './cmd/release-tool', '--dir', str(dest), '--key', str(key), '--version', version, '--sequence', str(seq), '--source', digest]
        if a.fixture and not key.exists():
            cmd.append('--create-key')
        run(cmd)
        (dest / 'install.sh').write_bytes((ROOT / 'scripts/install.sh').read_bytes())
        online_installer(dest, version)
        sums = {f.name: hashlib.sha256(f.read_bytes()).hexdigest() for f in dest.iterdir() if f.is_file() and f.name != 'SHA256SUMS'}
        (dest / 'SHA256SUMS').write_text(''.join(f'{v}  {k}\n' for k, v in sorted(sums.items())), encoding='utf-8')
        if label != 'bad':
            for arch in ['amd64', 'arm64']:
                package = a.output / f'filepanel-{version}-linux-{arch}.zip'
                with zipfile.ZipFile(package, 'w', zipfile.ZIP_DEFLATED) as z:
                    for name in ['filepanel-linux-' + arch, 'release.json', 'trusted.pub', 'install.sh', 'install-online.sh', 'SHA256SUMS']:
                        z.write(dest / name, name)
                    z.write(source, 'source.zip')
                    for name in ['docs/安装与本机卸载.md', 'docs/更新与备份恢复.md']:
                        if (ROOT / name).exists(): z.write(ROOT / name, Path(name).name)
    meta = {'source_zip_sha256': digest, 'git_base': run(['git', 'rev-parse', 'HEAD'], capture_output=True, text=True).stdout.strip(), 'working_tree_dirty': bool(run(['git', 'status', '--porcelain'], capture_output=True).stdout), 'fixture_only': a.fixture, 'versions': versions}
    (a.output / 'source.json').write_text(json.dumps(meta, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(meta))


if __name__ == '__main__':
    main()
