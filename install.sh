#!/bin/sh
# Installs or updates fzb from the latest release:
#
#	curl -fsSL https://raw.githubusercontent.com/okbytes/fzb/main/install.sh | sh
#
# It installs to /usr/local/bin, using sudo if needed; set FZB_DIR to change.
set -eu

dir=${FZB_DIR:-/usr/local/bin}

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) echo "fzb: no release for $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "fzb: no release for $(uname -m)" >&2; exit 1 ;;
esac

# Resolve the latest tag once, so the version printed is the one downloaded.
repo=https://github.com/okbytes/fzb
tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$repo/releases/latest")
tag=${tag##*/}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$repo/releases/download/$tag/fzb_${os}_${arch}.tar.gz" | tar -xz -C "$tmp" fzb

sudo=
if ! { mkdir -p "$dir" && [ -w "$dir" ]; } 2>/dev/null; then
	sudo=sudo
fi
$sudo mkdir -p "$dir"
$sudo install -m 755 "$tmp/fzb" "$dir/fzb"
echo "installed fzb $tag to $dir/fzb"
