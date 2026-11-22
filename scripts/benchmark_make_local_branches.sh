#!/usr/bin/env bash
# Prepare a clone for benchmark_startup.sh: give every branch of the remote "origin" a local branch at the same commit.
set -euo pipefail

cd "$1"
upstream=${2:-}

# A name with a quote or a backslash needs escaping in the config file
names=$(git for-each-ref --format='%(refname:lstrip=3)' refs/remotes/origin |
	grep -vxF HEAD |
	grep -v '["\\]')
existing=$(git for-each-ref --format='%(refname:lstrip=2)' refs/heads)
new=$(comm -23 <(sort <<<"$names") <(sort <<<"$existing"))

while IFS= read -r name; do
	printf 'create refs/heads/%s refs/remotes/origin/%s\n' "$name" "$name"
done <<<"$new" | git update-ref --stdin

# `git branch --set-upstream-to` rewrites the config file for each branch,
# which takes minutes for thousands of them. Append the sections in one go.
while IFS= read -r name; do
	printf '[branch "%s"]\n\tremote = origin\n\tmerge = refs/heads/%s\n' "$name" "${upstream:-$name}"
done <<<"$new" >>"$(git rev-parse --git-path config)"

echo "local branches: $(git for-each-ref refs/heads | wc -l)"
