#!/usr/bin/env bash

# This script updates the version of changie being downloaded by the miniscruff/changie-action action

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

newVer=${1}

while IFS= read -r -d '' file
do
  patch="$(diff -U0 -w -b --ignore-blank-lines "$file" <(VER="${newVer}" yq eval '(.jobs[].steps[] | select(.uses | match("miniscruff/changie-action@[\S]{2,40}")) | .with.version) = env(VER)' "$file") || true)"
  if [ "$patch" != "" ]; then
    patch "$file" <<< "$patch"
  fi
done < <(find .github/workflows -type f -name '*.yaml' -print0)
