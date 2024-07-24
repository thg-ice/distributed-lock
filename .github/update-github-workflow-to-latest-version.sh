#!/usr/bin/env bash

# This script updates the field of an action within .github/workflows, typically used to bump version numbers`

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

while IFS= read -r -d '' file
do
  patch="$(diff -U0 -w -b --ignore-blank-lines "$file" <(yq eval '(.jobs[].steps[] | select(.uses | match(env(ACTION) + "@[a-z0-9]{2,40}")) | select(.with | has(env(FIELD))) | .with[env(FIELD)]) = env(VALUE)' "$file") || true)"
  if [ "$patch" != "" ]; then
    patch "$file" <<< "$patch"
  fi
done < <(find .github/workflows -type f -name '*.yaml' -print0)
