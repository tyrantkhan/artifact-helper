#!/usr/bin/env bash
set -euo pipefail

# Checks that every place listing Conventional Commit types lists the same ones. Every merge to main cuts a
# release only when release-please shows its type, so a type one place allows and another drops either blocks
# a merge or ships nothing. The places: lefthook's commit-msg check, the PR-title workflow, the priority
# script, and release-please's visible changelog sections. Lefthook's branch check may list more, such as
# hotfix, but never fewer.

cd "$(dirname "$0")/../.."

# words prints its input's words one per line, sorted and unique.
words() { tr -s ' |,\n' '\n' | sed '/^$/d' | sort -u; }

commit_msg=$(grep -oE "grep -qE '\^\([a-z|]+\)\(" lefthook.yml | head -1 | sed -E "s/.*\^\(([a-z|]+)\).*/\1/" | words)
branch=$(grep -oE "grep -qE '\^\([a-z|]+\)/" lefthook.yml | head -1 | sed -E "s/.*\^\(([a-z|]+)\).*/\1/" | words)
pr_title=$(sed -n '/types: |/,/requireScope/p' .github/workflows/pr-title.yml | sed '1d;$d' | words)
priority=$(grep -E '^[[:space:]]+[a-z]+\) echo' .github/scripts/check-pr-title-priority.sh | sed -E 's/^[[:space:]]+([a-z]+)\).*/\1/' | words)
sections=$(jq -r '[.. | objects | select(has("type") and has("section")) | select(.hidden != true) | .type] | .[]' release-please-config.json | words)

if [[ -z "$commit_msg" ]]; then
  echo "::error::Could not read the commit types from lefthook.yml"
  exit 1
fi

status=0
for place in pr_title priority sections; do
  if [[ "${!place}" != "$commit_msg" ]]; then
    echo "::error::The commit types in $place differ from lefthook's commit-msg check:"
    diff <(echo "$commit_msg") <(echo "${!place}") || true
    status=1
  fi
done
if missing=$(comm -23 <(echo "$commit_msg") <(echo "$branch")) && [[ -n "$missing" ]]; then
  echo "::error::Lefthook's branch check does not allow: $missing"
  status=1
fi
if [[ "$status" == 0 ]]; then
  echo "All places list the same commit types: $(echo "$commit_msg" | tr '\n' ' ')"
fi
exit "$status"
