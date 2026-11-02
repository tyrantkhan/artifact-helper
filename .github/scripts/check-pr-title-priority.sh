#!/usr/bin/env bash
set -euo pipefail

# Ensures the PR title uses the highest-priority conventional commit type
# from its commits, so release-please picks up version-bumping changes.
#
# Required env vars: PR_TITLE, PR_NUMBER, GITHUB_REPOSITORY, GH_TOKEN

# Priority of a conventional commit type: lower is higher, 999 when unknown.
get_priority() {
  case "$1" in
    feat) echo 0 ;;
    fix) echo 1 ;;
    refactor) echo 2 ;;
    perf) echo 3 ;;
    docs) echo 4 ;;
    style) echo 5 ;;
    test) echo 6 ;;
    build) echo 7 ;;
    ci) echo 8 ;;
    chore) echo 9 ;;
    revert) echo 10 ;;
    *) echo 999 ;;
  esac
}

# Extract type from a conventional commit string (everything before '!', ':', or '(')
extract_type() {
  local msg="$1"
  echo "$msg" | sed -E 's/^([a-z]+)[!:(].*/\1/'
}

# --- PR title type ---
pr_type=$(extract_type "$PR_TITLE")
pr_priority=$(get_priority "$pr_type")

if [[ "$pr_priority" == "999" ]]; then
  echo "::warning::Could not parse PR title type '$pr_type' — skipping priority check"
  exit 0
fi

echo "PR title type: $pr_type (priority $pr_priority)"

# --- Commit types ---
highest_type="$pr_type"
highest_priority="$pr_priority"

commits=$(gh pr view "$PR_NUMBER" --repo "$GITHUB_REPOSITORY" --json commits --jq '.commits[].messageHeadline')

while IFS= read -r msg; do
  [[ -z "$msg" ]] && continue
  ctype=$(extract_type "$msg")
  cpri=$(get_priority "$ctype")

  if [[ "$cpri" == "999" ]]; then
    echo "  Skipping non-conventional commit: $msg"
    continue
  fi

  echo "  Commit type: $ctype (priority $cpri) — $msg"

  if (( cpri < highest_priority )); then
    highest_type="$ctype"
    highest_priority="$cpri"
  fi
done <<< "$commits"

# --- Compare ---
if (( highest_priority < pr_priority )); then
  echo ""
  echo "::error::PR title type '$pr_type' is lower priority than commit type '$highest_type'."
  echo "::error::Update the PR title to use '$highest_type' so release-please picks up the change."
  echo ""
  echo "Priority order: feat > fix > refactor > perf > docs > style > test > build > ci > chore > revert"
  exit 1
fi

echo ""
echo "PR title type '$pr_type' matches or outranks all commit types. OK."
