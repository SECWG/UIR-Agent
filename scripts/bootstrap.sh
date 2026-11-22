#!/bin/sh
set -eu
server='' token='' proxy='' data='' download_dir=./uir-agent foreground=false
if [ "$#" -eq 2 ]; then
  case "$1" in http://*|https://*) server=$1; token=$2; shift 2;; esac
fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = --foreground ]; then foreground=true; shift; continue; fi
  [ "$#" -ge 2 ] || { echo 'An option value is missing' >&2; exit 2; }
  case "$1" in
    --server) server=$2;;
    --join-token|--token) token=$2;;
    --proxy) proxy=$2;;
    --data-dir) data=$2;;
    --download-dir) download_dir=$2;;
    *) echo 'Unknown option' >&2; exit 2;;
  esac
  shift 2
done
server=${server%/}
case "$server" in http://*|https://*) ;; *) echo 'Provide --server with an HTTP or HTTPS origin' >&2; exit 2;; esac
case "${server#*://}" in ''|*/*|*@*|*\?*|*\#*) echo 'Use a server origin without a path or credentials' >&2; exit 2;; esac
case "$token" in ''|*[!a-zA-Z0-9_-]*) echo 'Invalid join code' >&2; exit 2;; esac
case "${#token}" in 16|43) ;; *) echo 'Invalid join code' >&2; exit 2;; esac
case "$(uname -s)" in Linux) platform=linux;; Darwin) platform=darwin;; *) echo 'Unsupported system' >&2; exit 2;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; i386|i486|i586|i686|x86) arch=386;; *) echo 'Unsupported architecture' >&2; exit 2;; esac
[ "$platform/$arch" != darwin/386 ] || { echo 'Unsupported architecture on macOS' >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo 'curl is required. Use Download enrollment in the platform.' >&2; exit 1; }
umask 077
mkdir -p "$download_dir"
temp=$(mktemp -d "$download_dir/.download.XXXXXX")
trap 'rm -f "$temp/uir-agent"; rmdir "$temp" 2>/dev/null || true' EXIT
binary=$temp/uir-agent
set -- -q -kfsS --connect-timeout 20 --max-time 300 -o "$binary" -w '%{http_code}' "$server/agent/download/$token/uir-agent-$platform-$arch"
[ -z "$proxy" ] || set -- "$@" --proxy "$proxy"
status=$(curl "$@") || {
  if [ "$status" = 404 ]; then
    echo "No Agent program published for $platform/$arch. Upload it in Agent management, then retry." >&2
  else
    echo 'Download failed. Check the address or generate a new connection command.' >&2
  fi
  exit 1
}
[ "$status" = 200 ] && [ -s "$binary" ] || { echo "Download failed: HTTP $status" >&2; exit 1; }
chmod 700 "$binary"
[ ! -d "$download_dir/uir-agent" ] || { echo 'Agent destination is a directory' >&2; exit 1; }
mv -f "$binary" "$download_dir/uir-agent"
set -- --server "$server" --join-token -
[ "$foreground" = false ] || set -- "$@" --foreground
[ -z "$proxy" ] || set -- "$@" --proxy "$proxy"
[ -z "$data" ] || set -- "$@" --data-dir "$data"
printf '%s\n' "$token" | "$download_dir/uir-agent" "$@"
