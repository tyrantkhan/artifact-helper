#!/bin/sh
# Copies the files the Claude Code plugin ships into a new directory, for the plugin branch that the plugin
# directory scans. The rest of the repository (the Go code, docs and installer) stays on main.
set -eu

out=${1:?usage: plugin-files.sh <new-output-dir>}
if [ -e "$out" ]; then
  echo "plugin-files.sh: $out already exists; pass a new directory" >&2
  exit 1
fi
root=$(git rev-parse --show-toplevel)

mkdir -p "$out/.claude-plugin" "$out/hooks"
cp -p "$root/.claude-plugin/plugin.json" "$out/.claude-plugin/"
cp -p "$root/hooks/hooks.json" "$root/hooks/session-start" "$out/hooks/"
mkdir -p "$out/skills"
cp -Rp "$root/skills/artifact-helper" "$out/skills/"
cp -p "$root/LICENSE" "$root/PRIVACY.md" "$out/"
cp -p "$root/.github/plugin-branch/README.md" "$out/"
