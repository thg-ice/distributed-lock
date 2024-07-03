#!/usr/bin/env bash

# Raise a PR and close any existing PRs where the title matches the prefix

# Exit on error. Append || true if you expect an error.
set -o errexit
# Exit on error inside any functions or subshells.
set -o errtrace
# Do not allow use of undefined vars. Use ${VAR:-} to use an undefined VAR
set -o nounset
# Catch the error in case mysqldump fails (but gzip succeeds) in `mysqldump |gzip`
set -o pipefail
# Turn on traces, useful while debugging but commented out by default
#set -o xtrace

prefix="${1}"

currentUser=$(gh api user | jq -r '.login')

existing=$(gh pr list --search "author:$currentUser state:open" --json title,number | \
  jq --arg t "$prefix" --raw-output '.[] | select(.title | contains($t)) | .number')

gh pr create --fill-first --label "skip changelog"

currentPr=$(gh pr view --json number | jq '.number')

echo "$existing" | jq -r '.[].number' | xargs -I % -n 1 gh pr close % --comment "Superseded by #${currentPr}" --delete-branch

echo "PR https://github.com/${GH_REPO}/pull/${currentPr} raised" >> "$GITHUB_STEP_SUMMARY"
