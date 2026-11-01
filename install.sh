#!/bin/sh
# install.sh: install the latest artifact-helper release into ~/.local/bin.
# Usage: curl -sSL https://raw.githubusercontent.com/tyrantkhan/claude-artifact-helper/main/install.sh | sh
set -eu

REPO="tyrantkhan/claude-artifact-helper"
NAME="artifact-helper"
INSTALL_DIR="${HOME}/.local/bin"
LATEST="https://github.com/${REPO}/releases/latest/download"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
    linux | darwin) ;;
    *) echo "Unsupported OS: $OS (this script covers macOS and Linux; Windows builds are on the releases page)" >&2; exit 1 ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64 | amd64) ARCH="amd64" ;;
    arm64 | aarch64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# The latest release's checksums name its archives, so one download gives both the version and the
# checksum to verify against, without the GitHub API and its rate limit.
curl -fsSL "${LATEST}/checksums.txt" -o "${TMPDIR}/checksums.txt"
LINE="$(grep "  ${NAME}_[0-9][^ ]*_${OS}_${ARCH}\.tar\.gz\$" "${TMPDIR}/checksums.txt" || true)"
if [ -z "$LINE" ]; then
    echo "The latest release has no archive for ${OS}/${ARCH}" >&2
    exit 1
fi
ARCHIVE="${LINE##* }"
VERSION="$(echo "$ARCHIVE" | sed "s/^${NAME}_\(.*\)_${OS}_${ARCH}\.tar\.gz\$/\1/")"

echo "Installing ${NAME} v${VERSION} (${OS}/${ARCH})..."
curl -fsSL "https://github.com/${REPO}/releases/download/v${VERSION}/${ARCHIVE}" -o "${TMPDIR}/${ARCHIVE}"

if command -v sha256sum >/dev/null 2>&1; then
    SUM="$(sha256sum "${TMPDIR}/${ARCHIVE}" | cut -d' ' -f1)"
else
    SUM="$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | cut -d' ' -f1)"
fi
if [ "$SUM" != "${LINE%% *}" ]; then
    echo "Checksum mismatch for ${ARCHIVE}; refusing to install it" >&2
    exit 1
fi

tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR" "$NAME"
mkdir -p "$INSTALL_DIR"
mv "${TMPDIR}/${NAME}" "${INSTALL_DIR}/${NAME}"
chmod +x "${INSTALL_DIR}/${NAME}"

echo "Installed ${NAME} ${VERSION} to ${INSTALL_DIR}/${NAME}"

case ":${PATH}:" in
    *":${INSTALL_DIR}:"*)
        echo "Run '${NAME} doctor' to check your Claude login."
        ;;
    *)
        echo "Add ${INSTALL_DIR} to your PATH, then run '${NAME} doctor' to check your Claude login."
        ;;
esac
