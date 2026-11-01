#!/bin/sh
# recom installer: downloads a release archive from GitHub, verifies its
# SHA-256 checksum, and installs the recom binary.
#
#   curl -fsSL https://raw.githubusercontent.com/chaseungjoon/recom/main/install.sh | sh
#
# Environment variables:
#   RECOM_VERSION      release to install, e.g. v0.1.0 (default: latest)
#   RECOM_INSTALL_DIR  directory for the binary (default: /usr/local/bin,
#                      falling back to ~/.local/bin when sudo is unavailable)

set -eu

REPO="chaseungjoon/recom"
BASE_URL="https://github.com/${REPO}/releases"

say() { printf 'recom-install: %s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

download() { # url dest
	if has curl; then
		curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
	elif has wget; then
		wget -q --https-only -O "$2" "$1"
	else
		die "curl or wget is required"
	fi
}

latest_tag() {
	# github.com/<repo>/releases/latest redirects to .../releases/tag/<tag>;
	# following the redirect avoids the rate-limited GitHub API.
	if has curl; then
		url=$(curl -fsSLI --proto '=https' -o /dev/null -w '%{url_effective}' "${BASE_URL}/latest")
	else
		url=$(wget -q --https-only --spider -S "${BASE_URL}/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1)
	fi
	tag=${url##*/}
	tag=$(printf '%s' "$tag" | tr -d '\r')
	case "$tag" in
		v[0-9]*) printf '%s\n' "$tag" ;;
		*) die "could not determine the latest release (got '${url}'); set RECOM_VERSION" ;;
	esac
}

sha256_of() {
	if has sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif has shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		die "sha256sum or shasum is required to verify the download"
	fi
}

detect_platform() {
	case "$(uname -s)" in
		Darwin) os=darwin ;;
		Linux) os=linux ;;
		*) die "unsupported OS: $(uname -s) (recom supports macOS and Linux)" ;;
	esac
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		arm64 | aarch64) arch=arm64 ;;
		*) die "unsupported architecture: $(uname -m) (recom supports amd64 and arm64)" ;;
	esac
	# Rosetta: an x86_64 shell on Apple Silicon should still get the native binary.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
		[ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
		arch=arm64
	fi
}

install_binary() { # src dir
	if [ -d "$2" ] && [ -w "$2" ]; then
		install -m 0755 "$1" "$2/recom"
	elif [ ! -e "$2" ] && mkdir -p "$2" 2>/dev/null; then
		install -m 0755 "$1" "$2/recom"
	elif has sudo; then
		say "installing to $2 requires sudo"
		sudo mkdir -p "$2"
		sudo install -m 0755 "$1" "$2/recom"
	else
		return 1
	fi
}

main() {
	detect_platform

	tag=${RECOM_VERSION:-}
	if [ -z "$tag" ]; then
		tag=$(latest_tag)
	fi
	case "$tag" in v*) ;; *) tag="v${tag}" ;; esac
	version=${tag#v}

	archive="recom_${version}_${os}_${arch}.tar.gz"
	say "installing recom ${tag} (${os}/${arch})"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t recom)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	download "${BASE_URL}/download/${tag}/${archive}" "$tmp/$archive" ||
		die "download failed: ${BASE_URL}/download/${tag}/${archive}"
	download "${BASE_URL}/download/${tag}/checksums.txt" "$tmp/checksums.txt" ||
		die "download failed: ${BASE_URL}/download/${tag}/checksums.txt"

	want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || die "${archive} is not listed in checksums.txt"
	got=$(sha256_of "$tmp/$archive")
	[ "$want" = "$got" ] || die "checksum mismatch for ${archive}: expected ${want}, got ${got}"
	say "checksum verified"

	tar -xzf "$tmp/$archive" -C "$tmp" recom
	[ -f "$tmp/recom" ] || die "archive did not contain the recom binary"

	dir=${RECOM_INSTALL_DIR:-/usr/local/bin}
	if ! install_binary "$tmp/recom" "$dir"; then
		if [ -n "${RECOM_INSTALL_DIR:-}" ]; then
			die "cannot write to ${dir}"
		fi
		dir="${HOME}/.local/bin"
		say "/usr/local/bin is not writable and sudo is unavailable; using ${dir}"
		install_binary "$tmp/recom" "$dir" || die "cannot write to ${dir}"
	fi

	say "installed ${dir}/recom"
	"${dir}/recom" version >&2 || true

	case ":${PATH}:" in
		*":${dir}:"*) ;;
		*) say "note: ${dir} is not on your PATH; add it, e.g. export PATH=\"${dir}:\$PATH\"" ;;
	esac

	if [ "$os" = darwin ]; then
		if ! rsync --version 2>/dev/null | head -n 1 | grep -q 'version 3'; then
			say "note: recom needs rsync 3.x locally; macOS ships an older one. Run: brew install rsync"
		fi
	elif ! has rsync; then
		say "note: recom needs rsync locally; install it with your package manager"
	fi
	has ssh || say "note: recom needs OpenSSH (ssh) locally"

	say "next: recom ssh <host>   (any ~/.ssh/config alias or user@host)"
}

main "$@"
