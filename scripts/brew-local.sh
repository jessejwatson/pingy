#!/usr/bin/env bash
# Install pingy through Homebrew from this working copy, using a local tap, so
# the formula can be tested before it's published.
#
#   scripts/brew-local.sh            build, install and run brew test
#   scripts/brew-local.sh uninstall  remove it and the local tap
set -euo pipefail

cd "$(dirname "$0")/.."
TAP="${USER:-me}/pingy-local"
VERSION="${VERSION:-0.1.0}"

if [[ "${1:-}" == "uninstall" ]]; then
  brew uninstall --formula "$TAP/pingy" 2>/dev/null || true
  brew untap "$TAP" 2>/dev/null || true
  exit 0
fi

make dist VERSION="$VERSION" >/dev/null
TARBALL="$PWD/dist/pingy-$VERSION.tar.gz"
SHA=$(shasum -a 256 "$TARBALL" | cut -d' ' -f1)

brew tap "$TAP" >/dev/null 2>&1 || brew tap-new --no-git "$TAP" >/dev/null
FORMULA="$(brew --repository "$TAP")/Formula/pingy.rb"
mkdir -p "$(dirname "$FORMULA")"
sed -e "s|^  url .*|  url \"file://$TARBALL\"|" \
    -e "s|^  sha256 .*|  sha256 \"$SHA\"|" \
    -e "/^  head /d" \
    Formula/pingy.rb > "$FORMULA"

# Reinstall cleanly each time so a rebuilt tarball is picked up.
brew uninstall --formula "$TAP/pingy" >/dev/null 2>&1 || true
HOMEBREW_NO_INSTALL_FROM_API=1 brew install --build-from-source "$TAP/pingy"
brew test "$TAP/pingy"
echo
echo "Installed: $(command -v pingy) ($(pingy --version))"
