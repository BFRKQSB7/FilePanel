"""Opt-in T07 service smoke. Runs on the development machine, never on a tiny VPS.

Provide --wsl Debian or an explicit SSH target/key/known-hosts. Only t07-panel,
t07-fail and the dedicated fixture directory are managed. Leaves instances on
failure for diagnosis; a passing run purges its instances and removes fixtures.
"""
import argparse
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


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--wsl')
    p.add_argument('--ssh-target')
    p.add_argument('--key')
    p.add_argument('--known-hosts')
    p.add_argument('--ssh-config', default=os.devnull)
    p.add_argument('--ssh-root', action='store_true')
    p.add_argument('--port', default='22')
    p.add_argument('--arch', choices=['amd64', 'arm64'], default='amd64')
    p.add_argument('--fixture', default=str(Path(__file__).resolve().parents[1] / 'dist/t07-fixture'))
    p.add_argument('--keep', action='store_true', help='保留实例供接下来的节点关联测试；完成后必须清理')
    a = p.parse_args()
    assert bool(a.wsl) != bool(a.ssh_target), 'exactly one target required'
    fixture = Path(a.fixture).resolve()
    remote = '/opt/filepanel-t07-fixture'
    binary = f'{remote}/filepanel-linux-{a.arch}'
    pin = hashlib.sha256((fixture / 'release.json').read_bytes()).hexdigest()
    args = []
    if a.ssh_target:
        assert a.key and a.known_hosts
        args = ['-F', a.ssh_config, '-i', a.key, '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + a.known_hosts, '-p', a.port]

    def shell(command, data=None, good=True):
        if a.wsl:
            argv = ['wsl.exe', '-d', a.wsl, '-u', 'root', '--', 'sh', '-c', command]
        else:
            elevation = '' if a.ssh_root or a.ssh_target.startswith('root@') else 'sudo -n '
            argv = ['ssh', *args, a.ssh_target, elevation + 'sh -c ' + shlex.quote(command)]
        r = subprocess.run(argv, input=data, capture_output=True, timeout=90)
        text = r.stdout.decode(errors='replace')
        if good and r.returncode:
            raise RuntimeError(f'command failed ({r.returncode}): {command}\n{text}\n{r.stderr.decode(errors="replace")}')
        if not good:
            assert r.returncode != 0, 'expected rejection: ' + command
        return text

    tunnel = None
    shell(f'umask 077; mkdir -p {remote}; test ! -e /etc/filepanel/t07-panel.json; test ! -e /etc/filepanel/t07-fail.json')
    if a.wsl:
        mount = '/mnt/' + fixture.drive[0].lower() + fixture.as_posix()[2:]
        shell(f'cp {shlex.quote(mount + "/filepanel-linux-" + a.arch)} {shlex.quote(mount + "/release.json")} {remote}/')
        local_port = 19870
    else:
        # Upload only into the login user's test directory, then copy as root.
        staging = '/root/filepanel-tests/t07-fixture' if a.ssh_root or a.ssh_target.startswith('root@') else '/home/ubuntu/filepanel-t07-fixture'
        subprocess.run(['ssh', *args, a.ssh_target, 'umask 077; mkdir -p ' + shlex.quote(staging)], check=True, capture_output=True)
        scp_args = args.copy()
        scp_args[scp_args.index('-p')] = '-P'
        subprocess.run(['scp', *scp_args, str(fixture / ('filepanel-linux-' + a.arch)), str(fixture / 'release.json'), a.ssh_target + ':' + staging + '/'], check=True, capture_output=True, timeout=120)
        shell(f'cp {staging}/filepanel-linux-{a.arch} {staging}/release.json {remote}/; rm -f {staging}/filepanel-linux-{a.arch} {staging}/release.json; rmdir {staging}')
        with socket.socket() as s:
            s.bind(('127.0.0.1', 0))
            local_port = s.getsockname()[1]
        tunnel = subprocess.Popen(['ssh', *args, '-N', '-o', 'ExitOnForwardFailure=yes', '-L', f'127.0.0.1:{local_port}:127.0.0.1:19870', a.ssh_target], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        time.sleep(1)
    shell(f'chmod 700 {binary}; command -v systemctl >/dev/null && echo systemd || echo openrc')
    install = f'{binary} install --instance t07-panel --listen 127.0.0.1:19870 --release {remote}/release.json --manifest-sha256 {pin}'
    ctl = f'{binary} maintain --instance t07-panel'
    uninstall = f'{binary} uninstall --instance t07-panel'
    base = f'http://127.0.0.1:{local_port}'
    cookies = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies), urllib.request.ProxyHandler({}))
    csrf = ''

    def req(path, value=None, method='GET', headers=None, raw=False):
        h = headers or {}
        if csrf:
            h['X-CSRF-Token'] = csrf
        if value is not None and not isinstance(value, bytes):
            value = json.dumps(value).encode()
            h['Content-Type'] = 'application/json'
        with client.open(urllib.request.Request(base + path, data=value, method=method, headers=h), timeout=20) as r:
            body = r.read()
            return body if raw else json.loads(body)

    try:
        shell(install.replace(pin, '0' * 64), good=False)
        print(shell(install).strip())
        token = shell(ctl + ' --action init-token').splitlines()[1]
        req('/api/init', {'token': token, 'password': 't07-test-password'}, 'POST')
        csrf = req('/api/login', {'password': 't07-test-password'}, 'POST')['csrf']
        data = bytes(range(256)) * 4096  # 1MiB; full 16MiB resource gate belongs to T08.
        boundary = 'filepanelT07'
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="t07.bin"\r\nContent-Type: application/octet-stream\r\n\r\n').encode() + data + f'\r\n--{boundary}--\r\n'.encode()
        item = req('/api/files', body, 'POST', {'Content-Type': 'multipart/form-data; boundary=' + boundary})
        assert hashlib.sha256(req('/api/files/' + item['id'], raw=True)).hexdigest() == hashlib.sha256(data).hexdigest()
        shell(install)
        assert req('/api/status')['authenticated']
        shell(ctl + ' --action restart')
        assert req('/api/files/' + item['id'], raw=True) == data
        ps = shell('ps -eo pid,args 2>/dev/null || ps -o pid,args')
        uid = shell('id -u fp-t07-panel').strip()
        pids = [line.split()[0] for line in ps.splitlines() if '/opt/filepanel-t07-panel/filepanel service-run' in line]
        assert len(pids) == 1 and pids[0].isdigit(), ps
        actual_uid = shell("awk '/^Uid:/{print $2}' /proc/" + pids[0] + '/status').strip()
        assert uid != '0' and actual_uid == uid, ps
        shell('test -e /etc/runlevels/default/filepanel-t07-panel || test -e /etc/systemd/system/multi-user.target.wants/filepanel-t07-panel.service')
        # Cancellation, symlink escape, changed directory identity, shared path.
        before = shell('sha256sum /etc/filepanel/t07-panel.json')
        assert '已取消' in shell(uninstall + ' --purge', b'no\n')
        assert before == shell('sha256sum /etc/filepanel/t07-panel.json')
        shell(f'touch {remote}/outside-sentinel; ln -s {remote}/outside-sentinel /var/lib/filepanel-t07-panel/escape')
        shell(uninstall + ' --purge', b'yes\n', good=False)
        shell(f'test -f {remote}/outside-sentinel; rm /var/lib/filepanel-t07-panel/escape')
        shell(f'{binary} install --instance t07-bad --data-dir /var/lib --release {remote}/release.json --manifest-sha256 {pin}', good=False)
        shell('mv /var/lib/filepanel-t07-panel /var/lib/filepanel-t07-panel-saved; mkdir /var/lib/filepanel-t07-panel; chown fp-t07-panel /var/lib/filepanel-t07-panel; chmod 700 /var/lib/filepanel-t07-panel')
        try:
            shell(uninstall + ' --purge', b'yes\n', good=False)
        finally:
            shell('rmdir /var/lib/filepanel-t07-panel; mv /var/lib/filepanel-t07-panel-saved /var/lib/filepanel-t07-panel')
        shell(ctl + ' --action /bin/sh', good=False)
        shell(f'su -s /bin/sh fp-t07-panel -c {shlex.quote(binary + " maintain --instance t07-panel --action restart")}', good=False)
        shell("su -s /bin/sh fp-t07-panel -c 'cat /etc/filepanel/t07-panel.json'", good=False)
        shell("su -s /bin/sh fp-t07-panel -c 'echo bad >> /opt/filepanel-t07-panel/runtime.json'", good=False)
        # Default uninstall preserves DB, credentials and file, then reinstalls.
        shell(uninstall, b'yes\n')
        assert '不在' not in shell(install)
        csrf = req('/api/login', {'password': 't07-test-password'}, 'POST')['csrf']
        assert req('/api/files/' + item['id'], raw=True) == data
        # Failed enrollment leaves a recoverable, disabled local instance.
        fail = f'{binary} install --instance t07-fail --role node --listen 127.0.0.1:19871 --release {remote}/release.json --manifest-sha256 {pin}'
        shell(fail, good=False)
        shell(f'{binary} uninstall --instance t07-fail --purge', b'yes\n')
        if not a.keep:
            shell(uninstall + ' --purge', b'yes\n')
            shell(f'test ! -e /var/lib/filepanel-t07-panel; test ! -e /opt/filepanel-t07-panel; test ! -e /etc/filepanel/t07-panel.json; test -f {remote}/outside-sentinel; rm {remote}/outside-sentinel {remote}/filepanel-linux-{a.arch} {remote}/release.json; rmdir {remote}')
        print('PASS: install/repeat/restart/non-root/autostart/cancel/symlink/identity/shared-path/maintenance/preserve-reinstall/purge/failure')
    finally:
        if tunnel:
            tunnel.terminate()
            tunnel.wait(timeout=10)


if __name__ == '__main__':
    main()
