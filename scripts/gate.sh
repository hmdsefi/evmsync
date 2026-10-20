#!/usr/bin/env bash
# Copyright 2026 Hamed Yousefi
# SPDX-License-Identifier: Apache-2.0

# The local mirror of CI. Run it before every commit; it checks the working tree as it is.
#
# usage: scripts/gate.sh          no arguments; exit 0 green, 1 red, 2 usage error
#   GATE_TCS   space-separated GOTOOLCHAIN values to run, default "local" (e.g. "local go1.26.0")
#   GATE_LOGS  log directory, default "<root>/tmp/gate"
#   GATE_LINT  golangci-lint binary, default "golangci-lint"
#
# It prints one PASS, FAIL or SKIP line per step and toolchain, then RESULT GREEN or RESULT RED.
# Each step's output goes to $GATE_LOGS/<go version>-<step>.log.

set -uo pipefail

if [ $# -ne 0 ]; then
	echo "usage: scripts/gate.sh (no arguments; set GATE_TCS, GATE_LOGS, GATE_LINT)" >&2
	exit 2
fi

unset GOROOT
export GOWORK=off

root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel) || exit 2
GATE_TCS=${GATE_TCS:-local}
GATE_LOGS=${GATE_LOGS:-$root/tmp/gate}
GATE_LINT=${GATE_LINT:-golangci-lint}

mkdir -p "$GATE_LOGS" || exit 2
GATE_LOGS=$(cd "$GATE_LOGS" && pwd) || exit 2
rm -f "$GATE_LOGS"/*.log
cd "$root" || exit 2

failures=0
v=

# step NAME CMD...: runs CMD with its output in the step's log and prints the step's result line.
# The first three failures of a run also print the interesting end of their log.
step() {
	local name=$1 log out
	shift
	log="$GATE_LOGS/$v-$name.log"
	if "$@" >"$log" 2>&1; then
		echo "PASS $name [$v]"
		return
	fi
	echo "FAIL $name [$v]"
	failures=$((failures + 1))
	if [ "$failures" -gt 3 ]; then
		return
	fi
	out=$(grep -E '^(--- FAIL|FAIL|panic:|# |.*\.go:[0-9]+|.*[Ee]rror|.*undefined|.*cannot )' "$log" | tail -n 25)
	if [ -z "$out" ]; then
		out=$(tail -n 25 "$log")
	fi
	if [ -n "$out" ]; then
		printf '%s\n' "$out" | sed 's/^/    /'
	fi
}

# run CMD...: writes "+ CMD" to the log, then runs CMD.
run() {
	echo "+ $*"
	"$@"
}

# check_gofmt lists the tracked and untracked Go files gofmt would change. git ls-files keeps
# gitignored trees such as .helper/ out of the walk.
check_gofmt() {
	local files bad
	files=$(git ls-files --cached --others --exclude-standard '*.go') || return 1
	if [ -z "$files" ]; then
		return 0
	fi
	# Word splitting on purpose, as in CI.
	# shellcheck disable=SC2086
	bad=$(gofmt -l $files) || {
		printf '%s\n' "$bad"
		return 1
	}
	if [ -n "$bad" ]; then
		printf '%s\n' "$bad"
		return 1
	fi
}

# check_nested tidies and vets each nested module and runs its tests, except e2e's, which need
# Anvil and run in their own CI job.
check_nested() {
	local dir rc=0
	for dir in $mods; do
		run go -C "$dir" mod tidy -diff || rc=1
		run go -C "$dir" vet ./... || rc=1
		if [ "$dir" != e2e ]; then
			run go -C "$dir" test -race -count=1 ./... || rc=1
		fi
	done
	return $rc
}

# check_lint runs golangci-lint at the root and in each nested module, after checking its version.
check_lint() {
	local version dir rc=0
	version=$("$GATE_LINT" version 2>&1)
	case "$version" in
	*"version 2.14.0 "* | *"version 2.14.0") ;;
	*)
		echo "golangci-lint v2.14.0 required, found: $(printf '%s\n' "$version" | head -n 1)"
		return 1
		;;
	esac
	run "$GATE_LINT" run ./... || rc=1
	for dir in $mods; do
		(cd "$dir" && run "$GATE_LINT" run ./...) || rc=1
	done
	return $rc
}

mods=$(git ls-files '*/go.mod')
if [ -n "$mods" ]; then
	mods=$(printf '%s\n' "$mods" | xargs -n 1 dirname | sort)
fi

first=1
for tc in $GATE_TCS; do
	export GOTOOLCHAIN=$tc
	v=$(go env GOVERSION 2>/dev/null) || v=
	if [ -z "$v" ]; then
		v=$tc
	fi
	step gofmt check_gofmt
	step tidy go mod tidy -diff
	step vet go vet ./...
	step build env CGO_ENABLED=0 go build ./...
	step test env CGO_ENABLED=0 go test -count=1 ./...
	step race go test -race -count=1 ./...
	if [ -z "$mods" ]; then
		echo "no nested module" >"$GATE_LOGS/$v-nested.log"
		echo "SKIP nested [$v] (no nested module)"
	else
		step nested check_nested
	fi
	if [ "$first" = 1 ]; then
		step lint check_lint
		first=0
	fi
done

if [ "$failures" -eq 0 ]; then
	echo "RESULT GREEN"
	exit 0
fi
echo "RESULT RED"
exit 1
