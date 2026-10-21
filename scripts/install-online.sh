#!/bin/sh
# Release template: build-release.py pins every downloaded executable before use.
set -eu
umask 077
version='@VERSION@'
manifest_sha='@MANIFEST_SHA256@'
update_key='@UPDATE_KEY@'
base=${FILEPANEL_RELEASE_BASE:-https://github.com/BFRKQSB7/FilePanel/releases/download/v$version}

fail() { printf '%s\n' "$*" >&2; exit 1; }
usage() {
    printf '%s\n' 'Usage: sudo sh install-online.sh [--instance main] [--listen 127.0.0.1:8080] [--low-ports]' \
        '       sudo sh install-online.sh --instance node --role node --join-file /private/join.json' \
        'Supports Alpine/OpenRC, Debian/systemd, Ubuntu 24.04/systemd; amd64/arm64.' \
        'Additional options: --data-dir PATH, --reserve-bytes BYTES.' \
        'The installer does not change firewall, DNS, or existing web servers.'
}
instance=main
role=panel
parse() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --help|-h) usage; exit 0 ;;
            --low-ports|--low-ports=true|--low-ports=false) shift ;;
            --instance|--role|--listen|--data-dir|--join-file|--reserve-bytes)
                [ "$#" -ge 2 ] && [ -n "$2" ] || fail "Missing value: $1"
                case "$1" in --instance) instance=$2 ;; --role) role=$2 ;; esac
                shift 2 ;;
            *) fail "Unsupported option: $1 (use --help)" ;;
        esac
    done
}
parse "$@"
[ "$(id -u)" = 0 ] || fail 'Run this script as root (sudo sh install-online.sh).'
case "$instance" in ''|[!a-z]*|*[!a-z0-9-]*) fail 'Invalid instance name.' ;; esac
[ "${#instance}" -le 20 ] || fail 'Instance name exceeds 20 characters.'
case "$role" in panel) listen=127.0.0.1:8080 ;; node) listen=127.0.0.1:8081 ;; *) fail 'Role must be panel or node.' ;; esac
case "$version" in *@*) fail 'Use the generated installer from a release, not the source template.' ;; esac
case "$base" in https://*) ;; *) fail 'Release source must use HTTPS.' ;; esac
base=${base%/}
[ "$(uname -s)" = Linux ] || fail 'Only Linux is supported.'
case "$(uname -m)" in
    x86_64|amd64) arch=amd64; binary_sha='@AMD64_SHA256@' ;;
    aarch64|arm64) arch=arm64; binary_sha='@ARM64_SHA256@' ;;
    *) fail 'Only amd64 and arm64 are supported.' ;;
esac
[ -r /etc/os-release ] || fail 'Cannot identify Linux distribution.'
. /etc/os-release
case "$ID" in
    alpine) [ -d /run/openrc ] && command -v rc-service >/dev/null || fail 'OpenRC must be running.' ;;
    debian) [ -d /run/systemd/system ] || fail 'systemd must be running.' ;;
    ubuntu) [ "$VERSION_ID" = 24.04 ] && [ -d /run/systemd/system ] || fail 'Ubuntu 24.04 with systemd is required.' ;;
    *) fail "Unsupported distribution: $ID" ;;
esac
command -v sha256sum >/dev/null || fail 'sha256sum is required.'
if command -v curl >/dev/null; then
    download() { curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 300 --retry 2 --output "$2" "$1"; }
elif command -v wget >/dev/null; then
    download() { wget -q -T 60 -O "$2" "$1"; }
else
    fail 'Install curl or wget with HTTPS support first.'
fi
verify() {
    actual=$(sha256sum "$1")
    [ "${actual%% *}" = "$2" ] || fail "SHA256 verification failed: ${1##*/}"
}
[ -d /opt ] && [ ! -L /opt ] || fail '/opt must be a real directory.'
work=$(mktemp -d /opt/filepanel-download.XXXXXX)
trap 'rm -rf -- "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
binary="$work/filepanel-linux-$arch"
printf 'Downloading FilePanel %s (%s)...\n' "$version" "$arch"
# Bound downloads without leaking a file-size limit into the installed service.
(ulimit -f 2048; download "$base/release.json" "$work/release.json")
verify "$work/release.json" "$manifest_sha"
(ulimit -f 262144; download "$base/filepanel-linux-$arch" "$binary")
verify "$binary" "$binary_sha"
chmod 700 "$binary"
fresh=false
[ -e "/etc/filepanel/$instance.json" ] || fresh=true
"$binary" install --listen "$listen" "$@" --release "$work/release.json" --manifest-sha256 "$manifest_sha" --update-key "$update_key"
if [ "$role" = panel ] && [ "$fresh" = true ]; then
    "$binary" maintain --instance "$instance" --action init-token
fi
