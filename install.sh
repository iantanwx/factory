#!/bin/sh
# Install the loop, or upgrade it, from the newest signed release:
#
#   gh release download -R slothware-labs/factorio -p install.sh -O - | sh
#
# Arguments after `sh -s --` go to `loop install`, for example
# `sh -s -- --github-owner acme`. LOOP_VERSION picks a release, and
# LOOP_INSTANCE an instance directory. A host that already runs the loop
# gets `loop upgrade` instead, which verifies the release's signature.
set -eu

repo=${LOOP_REPO:-slothware-labs/factorio}
instance=${LOOP_INSTANCE:-$HOME/.local/state/loop}
cache=${XDG_CACHE_HOME:-$HOME/.cache}/loop/releases

command -v gh >/dev/null 2>&1 || {
	echo "install.sh needs the GitHub CLI, logged in: https://cli.github.com" >&2
	exit 1
}
case "$(uname -s)/$(uname -m)" in
Linux/x86_64) asset=loop-linux-amd64 ;;
Linux/aarch64 | Linux/arm64) asset=loop-linux-arm64 ;;
Darwin/arm64) asset=loop-darwin-arm64 ;;
*)
	echo "no loop release for $(uname -s)/$(uname -m)" >&2
	exit 1
	;;
esac

version=${LOOP_VERSION:-$(gh release view -R "$repo" --json tagName -q .tagName)}
dir=$cache/$version
mkdir -p "$dir"
gh release download "$version" -R "$repo" -D "$dir" --clobber \
	-p "$asset" -p checksums.txt -p checksums.txt.minisig -p release.json
(
	cd "$dir"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c --ignore-missing --quiet checksums.txt
	else
		grep " $asset\$" checksums.txt | shasum -a 256 -c --quiet
	fi
)
chmod +x "$dir/$asset"

if [ -x "$instance/current/loop" ]; then
	LOOP_RELEASE_BASE=$cache exec "$instance/current/loop" upgrade "$version"
fi
# The GitHub App step reads a pasted address from the terminal; this
# script's own stdin is the pipe it came through.
if (: </dev/tty) 2>/dev/null; then
	exec "$dir/$asset" install "$@" </dev/tty
fi
exec "$dir/$asset" install "$@"
