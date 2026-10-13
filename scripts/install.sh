#!/bin/sh
# shellcheck shell=dash
# kyacode installer - POSIX form. Non-interactive by construction: it
# runs under `curl -fsSL <entrance> | sh`, so nothing here may prompt,
# elevate, or edit rc files. A failed download or checksum leaves any
# existing install untouched and exits non-zero.
#
#   curl -fsSL https://kyaky.ai/kyacode/install.sh | sh
#   curl -fsSL https://kyaky.ai/kyacode/install.sh | KYA_VERSION=v4.4.0 sh
#   curl -fsSL https://kyaky.ai/kyacode/install.sh | sh -s v4.4.0
#
# Environment:
#   KYA_VERSION           pin a tag (default latest)
#   KYACODE_INSTALL_DIR   override the install dir (default ~/.local/bin)

set -u

# The Asset base: the dist directory the kyaky.ai site deployment
# serves. One place to flip - the cross-platform guidance below derives
# from it, so the printed URL can never drift from the download origin.
ASSET_BASE="https://kyaky.ai/kyacode"

fail() {
	printf 'kyacode: %s\n' "$*" >&2
	exit 1
}

[ "$(id -u)" != "0" ] ||
	fail "refusing to run as root - this installer places the binary in your own home; run it as yourself"

command -v curl >/dev/null 2>&1 ||
	fail "curl is required (macOS, Ubuntu and Git-for-Windows all ship it)"

# --- platform: refuse before any network call ---
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os" in
	darwin | linux) ;;
	msys* | mingw*)
		fail "this is the POSIX installer; on Windows run: irm $ASSET_BASE/install.ps1 | iex"
		;;
	*) fail "unsupported OS '$os' (this installer supports darwin and linux)" ;;
esac
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported architecture '$arch' (need x86_64 or arm64/aarch64)" ;;
esac
asset="kyacode-$os-$arch"

# --- version pin: positional wins over env; 'latest' or a vX.Y.Z tag ---
version="${KYA_VERSION:-latest}"
if [ "${1:-}" != "" ]; then
	[ $# -eq 1 ] || fail "usage: sh -s <version> (one argument: latest or vX.Y.Z)"
	version="$1"
fi
case "$version" in
	latest) urlbase="$ASSET_BASE" ;;
	v[0-9]* | [0-9]*)
		case "$version" in [0-9]*) version="v$version" ;; esac
		case "${version#v}" in
			*[!0-9.]* | *..* | *.) fail "bad version tag '$version' (need latest or vX.Y.Z)" ;;
		esac
		urlbase="$ASSET_BASE/$version"
		;;
	*) fail "bad version '$version' (need latest or vX.Y.Z)" ;;
esac

install_dir="${KYACODE_INSTALL_DIR:-$HOME/.local/bin}"

tmp=$(mktemp -d) || fail "mktemp failed"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' INT TERM

echo "kyacode: downloading $asset ($version) ..."
curl -fsSL -o "$tmp/$asset" "$urlbase/$asset" ||
	fail "binary download failed ($urlbase/$asset)"
curl -fsSL -o "$tmp/$asset.sha256" "$urlbase/$asset.sha256" ||
	fail "checksum download failed ($urlbase/$asset.sha256)"

(cd "$tmp" && {
	sha256sum --check "$asset.sha256" 2>/dev/null ||
		shasum -a 256 --check "$asset.sha256"
}) || fail "checksum mismatch - refusing to install"

mkdir -p "$install_dir" || fail "cannot create $install_dir"
# stage beside the final name, then rename: the swap into place is atomic
install -m 0755 "$tmp/$asset" "$install_dir/.kyacode.tmp.$$" ||
	fail "could not write into $install_dir"
mv -f "$install_dir/.kyacode.tmp.$$" "$install_dir/kyacode" ||
	fail "could not move the binary into place"

newbin="$install_dir/kyacode"
resolved=$(command -v kyacode 2>/dev/null || true)
if [ -n "$resolved" ] && [ "$resolved" != "$newbin" ]; then
	printf 'kyacode: WARNING: an existing kyacode resolves to %s - it may keep winning PATH resolution over %s\n' "$resolved" "$newbin" >&2
fi
case ":$PATH:" in
	*":$install_dir:"*) ;;
	*)
		printf 'kyacode: %s is not on PATH; paste this to use it now:\n  export PATH="%s:$PATH"\n' "$install_dir" "$install_dir"
		;;
esac

printf 'kyacode: installed %s\n' "$newbin"
"$newbin" version ||
	printf 'kyacode: WARNING: the installed binary did not run cleanly\n' >&2
printf 'kyacode: next steps:\n  kyacode auth     # paste your gateway key\n  kyacode doctor   # pre-flight checks\n  kyacode launch <harness> <model>\n'
