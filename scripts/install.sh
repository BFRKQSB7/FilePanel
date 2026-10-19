#!/bin/sh
# Offline bootstrap: verify the executable before running it as administrator.
set -eu
if [ "$#" -lt 2 ]; then
    echo '用法: sh install.sh /可信路径/filepanel 二进制SHA256 install --release /可信路径/release.json --manifest-sha256 清单SHA256 [参数]' >&2
    exit 1
fi
binary=$1
expected=$2
shift 2
case "$binary" in /*) ;; *) echo '二进制须为绝对路径' >&2; exit 1;; esac
case "$expected" in *[!0-9a-f]*|'') echo 'SHA256 格式无效' >&2; exit 1;; esac
[ "${#expected}" -eq 64 ] || exit 1
[ -f "$binary" ] && [ ! -L "$binary" ] || exit 1
actual=$(sha256sum "$binary")
actual=${actual%% *}
[ "$actual" = "$expected" ] || { echo '二进制 SHA256 不匹配' >&2; exit 1; }
exec "$binary" "$@"
