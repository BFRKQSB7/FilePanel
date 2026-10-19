"""Opt-in installed T08 release smoke, driven from the development machine.

Only manages t08-panel and its explicit fixture/snapshot directories. Failure
leaves the instance for diagnosis; no production release or DNS mutation.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--wsl')
    p.add_argument('--ssh-target')
    p.add_argument('--key')
    p.add_argument('--known-hosts')
    p.add_argument('--port', default='22')
    p.add_argument('--arch', default='amd64', choices=['amd64', 'arm64'])
    p.add_argument('--resource', action='store_true')
    p.add_argument('--public-base', help='资源验收的实际 NAT/公网入口；从当前测试环境文件核对后显式传入')
    p.add_argument('--expected-host', help='资源验收的预期主机名；从当前测试环境文件核对后显式传入')
    p.add_argument('--keep', action='store_true')
    p.add_argument('--resume', action='store_true')
    p.add_argument('--output', type=Path)
    p.add_argument('--fixture', type=Path, default=ROOT / 'dist/t08-release')
    a = p.parse_args()
    assert bool(a.wsl) != bool(a.ssh_target)
    assert not a.resource or (a.public_base and a.expected_host), 'resource smoke requires an explicit public base and expected hostname'
    fixture = a.fixture.resolve()
    remote = '/opt/filepanel-t08-fixture'
    q = shlex.quote
    ssh = ['-F', os.devnull, '-i', a.key or '', '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + (a.known_hosts or ''), '-p', a.port]

    def shell(command, data=None, good=True, timeout=120):
        command = 'set -eu\n' + command
        if a.wsl:
            argv = ['wsl.exe', '-d', a.wsl, '-u', 'root', '--', 'sh', '-c', command]
        else:
            elevation = '' if a.ssh_target.startswith('root@') else 'sudo -n '
            argv = ['ssh', *ssh, a.ssh_target, elevation + 'sh -c ' + q(command)]
        r = subprocess.run(argv, input=data, capture_output=True, timeout=timeout)
        if good and r.returncode:
            raise RuntimeError(f'{command}\n{r.stdout.decode(errors="replace")}\n{r.stderr.decode(errors="replace")}')
        if not good:
            assert r.returncode != 0, command
        return r.stdout.decode(errors='replace')

    facts = shell('hostname; cat /etc/os-release; if test -f /sys/fs/cgroup/memory.max; then cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.current; else head -n 3 /proc/meminfo; fi; df -k /; cat /proc/swaps; ps -p 1 -o comm= 2>/dev/null || true')
    print(facts, flush=True)
    if a.resource:
        assert a.expected_host in facts and '134217728' in facts and 'Alpine' in facts
    if not a.resume:
        shell(f'test ! -e /etc/filepanel/t08-panel.json; umask 077; mkdir -p {remote}')
        for version in ['v1', 'v2', 'bad']:
            shell(f'mkdir -p {remote}/{version}')
            for name in ['filepanel-linux-' + a.arch, 'release.json', 'trusted.pub']:
                path = fixture / version / name
                # Bytes are streamed over SSH stdin; no executable shell data.
                shell(f'cat > {q(remote + "/" + version + "/" + name)}', path.read_bytes())
        shell(f'chmod 700 {remote}/*/filepanel-linux-{a.arch}')
    binary = f'{remote}/v1/filepanel-linux-{a.arch}'
    ctl = binary + ' maintain --instance t08-panel'
    installed = '/opt/filepanel-t08-panel/filepanel'
    config = '/opt/filepanel-t08-panel/config'
    data_dir = '/var/lib/filepanel-t08-panel'
    pin = hashlib.sha256((fixture / 'v1/release.json').read_bytes()).hexdigest()
    public_key = (fixture / 'v1/trusted.pub').read_text().strip()
    if not a.resume:
        print(shell(f'{binary} install --instance t08-panel --listen 0.0.0.0:80 --low-ports --release {remote}/v1/release.json --manifest-sha256 {pin} --update-key {q(public_key)}' if a.resource else f'{binary} install --instance t08-panel --listen 127.0.0.1:19870 --release {remote}/v1/release.json --manifest-sha256 {pin} --update-key {q(public_key)}'), flush=True)
    port = '80' if a.resource else '19870'
    tunnel = None
    if a.wsl or a.resource:
        local_port = int(port)
    else:
        with socket.socket() as s:
            s.bind(('127.0.0.1', 0)); local_port = s.getsockname()[1]
        tunnel = subprocess.Popen(['ssh', *ssh, '-N', '-o', 'ExitOnForwardFailure=yes', '-L', f'127.0.0.1:{local_port}:127.0.0.1:{port}', a.ssh_target], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        deadline = time.monotonic() + 20
        while True:
            assert tunnel.poll() is None, 'SSH tunnel exited before becoming ready'
            try:
                with socket.create_connection(('127.0.0.1', local_port), timeout=1):
                    break
            except OSError:
                if time.monotonic() >= deadline:
                    raise TimeoutError('SSH forwarding listener did not become ready')
                time.sleep(0.2)
    base = a.public_base.rstrip('/') if a.resource else f'http://127.0.0.1:{local_port}'
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    csrf = ''

    def req(path, value=None, method='GET', headers=None, raw=False):
        h = dict(headers or {})
        if csrf: h['X-CSRF-Token'] = csrf
        if value is not None and not isinstance(value, bytes):
            value = json.dumps(value).encode(); h['Content-Type'] = 'application/json'
        with client.open(urllib.request.Request(base + path, data=value, method=method, headers=h), timeout=60) as r:
            body = r.read()
            return body if raw else json.loads(body)

    def wait_update(expected):
        deadline = time.monotonic() + 65
        while time.monotonic() < deadline:
            try:
                s = req('/api/update')
                if s.get('phase') in expected: return s
            except Exception:
                pass
            time.sleep(1)
        raise AssertionError('update did not reach expected state')

    result = {'environment': facts, 'arch': a.arch, 'fixture_sha256': hashlib.sha256((fixture / 'v1/release.json').read_bytes()).hexdigest(), 'checks': [], 'resource_required': a.resource}
    result['resource_observations'] = []
    def observe(label):
        result['resource_observations'].append({'phase': label, 'values': shell('date +%s; cat /sys/fs/cgroup/cpu.max 2>/dev/null || true; for p in /proc/[0-9]*; do exe=$(readlink "$p/exe" 2>/dev/null || true); case "$exe" in /opt/filepanel-t08-panel/filepanel*) echo PROCESS "$p"; awk \'/^(Name|VmRSS|VmHWM):/{print}\' "$p/status";; esac; done; if test -d /run/systemd/system; then systemctl show filepanel-t08-panel.service filepanel-t08-panel-update.service -p MemoryCurrent -p MemoryPeak -p CPUUsageNSec; fi; df -k /')})
    sampler = None
    try:
        if not req('/api/status')['initialized']:
            token = shell(ctl + ' --action init-token').splitlines()[1]
            req('/api/init', {'token': token, 'password': 't08-local-test-password'}, 'POST')
        csrf = req('/api/login', {'password': 't08-local-test-password'}, 'POST')['csrf']
        data = bytes(range(256)) * (65536 if a.resource else 4096)
        digest = hashlib.sha256(data).hexdigest()
        boundary = 'FilePanelT08'
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="t08.bin"\r\nContent-Type: application/octet-stream\r\n\r\n').encode() + data + f'\r\n--{boundary}--\r\n'.encode()
        item = req('/api/files', body, 'POST', {'Content-Type': 'multipart/form-data; boundary=' + boundary})
        assert item['sha256'] == digest
        assert hashlib.sha256(req('/api/files/' + item['id'], raw=True)).hexdigest() == digest
        share = req('/api/files/' + item['id'] + '/share', {'enabled': True, 'featured': True, 'title': 'T08 release smoke'}, 'PUT')
        assert b'T08 release smoke' in req('/', raw=True)
        public = '/s/' + share['slug'] + '/get'
        assert hashlib.sha256(req(public, raw=True)).hexdigest() == digest
        assert req(public, headers={'Range': 'bytes=17-47'}, raw=True) == data[17:48]
        observe('after_upload_download')
        result['checks'].append('init/login/upload/private/public/hash/range')
        if a.resource:
            # Package NAT mapping 45002 -> 80, independent of SSH forwarding.
            public_client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with public_client.open(base + public, timeout=60) as r:
                assert hashlib.sha256(r.read()).hexdigest() == digest
            result['checks'].append('real_NAT_45002_to_80_public_hash')
            sampler_body = '''#!/bin/sh
while :; do
 echo SAMPLE $(date +%s)
 cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak /sys/fs/cgroup/memory.swap.current /sys/fs/cgroup/memory.events /sys/fs/cgroup/cpu.stat
 df -k /
 for p in $(pidof filepanel); do echo PROCESS $p; awk '/^(Name|VmRSS|VmHWM):/{print}' /proc/$p/status; done
 sleep 2
done
'''
            shell(f'cat > {remote}/sample.sh', sampler_body.encode())
            sampler = shell(f'nohup sh {remote}/sample.sh < /dev/null > {remote}/metrics.txt 2>&1 & echo $!').strip()
            print('Resource idle observation: 600 seconds', flush=True)
            start = time.monotonic()
            for _ in range(10):
                time.sleep(60); assert req('/healthz')['version'] == '0.1.0'; print('idle elapsed', round(time.monotonic()-start), flush=True)
            result['idle_seconds'] = time.monotonic() - start

            def slow_download(_):
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                begin = time.monotonic(); h = hashlib.sha256()
                with opener.open(base + public, timeout=60) as r:
                    while True:
                        chunk = r.read(128 << 10)
                        if not chunk: break
                        h.update(chunk); time.sleep(0.3)
                assert h.hexdigest() == digest
                return time.monotonic()-begin
            with ThreadPoolExecutor(2) as pool:
                durations = list(pool.map(slow_download, range(2)))
            assert min(durations) >= 30
            result['overlapping_download_seconds'] = durations
            result['checks'].append('600s_idle/two_overlapping_16MiB_downloads')
        # Pre-update snapshot contains publication state and admin identity.
        backup_path = remote + '/snapshot'
        print(shell(ctl + ' --action backup --backup-path ' + backup_path), flush=True)
        assert req('/api/files/' + item['id'], raw=True) == data
        result['checks'].append('installed_consistent_backup')
        print(shell(ctl + f' --action stage-update --release {remote}/v2/release.json'), flush=True)
        assert req('/api/update')['phase'] == 'staged'
        req('/api/update', method='POST')
        state = wait_update({'complete', 'failed', 'rolled_back'})
        assert state['phase'] == 'complete', state
        assert req('/healthz')['version'] == '0.1.1'
        assert req('/api/files/' + item['id'], raw=True) == data
        assert req('/api/files/' + item['id'] + '/share')['slug'] == share['slug']
        observe('after_update')
        result['checks'].append('web_signed_update_0.1.0_to_0.1.1/hash/slug/session')
        # Rollback requests are rejected before maintenance.
        shell(ctl + f' --action stage-update --release {remote}/v1/release.json', good=False)
        # The confirmed snapshot and previous binary are deliberately retained.
        # This isolated smoke archives/removes only its own now-verified copies.
        shell(f'test -d {config}/update-snapshot; test -f {config}/previous-binary; rm -rf {config}/update-snapshot; rm {config}/previous-binary')
        print(shell(ctl + f' --action stage-update --release {remote}/bad/release.json'), flush=True)
        req('/api/update', method='POST')
        state = wait_update({'failed', 'rolled_back'})
        assert req('/healthz')['version'] == '0.1.1', state
        assert req('/api/files/' + item['id'], raw=True) == data
        # Repeat recovery must be a no-op, retaining all current data.
        shell(ctl + ' --action recover-update')
        shell(ctl + ' --action recover-update')
        result['checks'].append('failed_restart_rollback/recovery_idempotent')
        # Remove this smoke's retained failure copies before the explicit restore.
        shell(f'rm -rf {config}/update-snapshot {config}/staged {data_dir}.previous.rollback; rm {config}/previous-binary')
        req('/api/files/' + item['id'], {'name': 'changed-after-backup.bin'}, 'PATCH')
        print(shell(ctl + ' --action restore --backup-path ' + backup_path), flush=True)
        assert req('/api/files/' + item['id'], raw=True) == data
        listing = req('/api/files'); listing = listing['files'] if isinstance(listing, dict) else listing
        assert next(v for v in listing if v['id'] == item['id'])['name'] == 't08.bin'
        assert req('/api/files/' + item['id'] + '/share')['enabled']
        observe('after_restore')
        result['checks'].append('explicit_restore/hash/metadata/public_state/identity')
        print(shell(ctl + ' --action restart'), flush=True)
        assert req('/api/files/' + item['id'], raw=True) == data
        req('/api/files/' + item['id'], method='DELETE')
        result['checks'].append('restart/data_read/delete')
        if sampler:
            shell('kill ' + sampler)
            sampler = None
            result['resource_samples'] = shell(f'cat {remote}/metrics.txt')
        result['status'] = 'PASS'
        if not a.keep:
            print(shell(installed + ' uninstall --instance t08-panel --purge', b'yes\n'), flush=True)
            shell(f'test ! -e /etc/filepanel/t08-panel.json; test ! -e {data_dir}; test ! -e /opt/filepanel-t08-panel; rm -rf {data_dir}.previous')
            result['checks'].append('uninstall_purge_both_services')
        output = a.output or ROOT / ('dist/t08-' + ('resource' if a.resource else a.wsl or a.arch) + '.json')
        output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')
        print('PASS', result['checks'], flush=True)
    finally:
        if sampler:
            shell('kill ' + sampler)
        if tunnel:
            tunnel.terminate(); tunnel.wait(timeout=10)


if __name__ == '__main__':
    main()
