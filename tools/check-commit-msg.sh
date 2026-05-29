#!/bin/sh
set -eu

msg_file=${1:?usage: check-commit-msg.sh <commit-msg-file>}
subject=$(sed -n '1p' "$msg_file")

case "$subject" in
	Merge\ *|Revert\ *|fixup!\ *|squash!\ *)
		exit 0
		;;
esac

if printf '%s\n' "$subject" | grep -Eq '^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-z0-9._/-]+\))?!?: .+'; then
	exit 0
fi

cat >&2 <<EOF
invalid commit subject: $subject

Use conventional commits:
  feat(scope): add thing
  fix(scope): repair thing
  refactor(scope): move code without behavior changes
  test(scope): cover behavior
  docs(scope): update docs
EOF
exit 1

