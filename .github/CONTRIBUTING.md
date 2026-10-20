# Contributing to evmsync

This page covers how to build and test evmsync, where code goes, the rules that keep a consumer's
state equal to the final canonical chain, and how to send a change.

For a bug fix or a small change, send a pull request. For a new feature, a new package or a change
in behavior, open an issue first, so we can agree on the design before you write the code.

## Development setup

You need Go 1.26 or newer and golangci-lint v2.14.0. golangci-lint v2.7.2 and other releases built
with Go 1.25 refuse to load a `go 1.26.0` module.

```shell
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
```

Before every commit, run the gate from the repository root:

```shell
scripts/gate.sh
```

It runs the same checks as CI on your working tree: gofmt, `go mod tidy -diff`, `go vet`, the
build and tests with `CGO_ENABLED=0`, the tests with the race detector, the nested modules and
golangci-lint. It prints one `PASS`, `FAIL` or `SKIP` line per step and ends with `RESULT GREEN` (exit 0)
or `RESULT RED` (exit 1). Three environment variables change what it runs:

| Variable | Default | Meaning |
|---|---|---|
| `GATE_TCS` | `local` | Space-separated `GOTOOLCHAIN` values to run the steps with, for example `local go1.26.0` |
| `GATE_LOGS` | `tmp/gate` under the repository root | Directory for each step's log, `<go version>-<step>.log` |
| `GATE_LINT` | `golangci-lint` | The golangci-lint binary; the gate checks that it is v2.14.0 |

CI runs these checks on Linux and macOS with Go 1.26 and 1.27.

## Repository layout

Each directory is one package with one job; `pgstore`, `e2e` and `examples` are nested modules
with their own `go.mod`. The version is the release a package first ships in.

| Package | Holds | Version |
|---|---|---|
| `chain` | Block references, headers, logs, filters, blooms and the `Source` interface | v0.1 |
| `chainlog` | The canonical log: entries, offsets, the `Log` interface, the validator `State`, the record codec and an in-memory log | v0.1 |
| `chainlog/wal` | A `chainlog.Log` stored in segment files | v0.1 |
| `chainlog/logtest` | The conformance suite for `chainlog.Log` implementations | v0.1 |
| `tracker` | Reorg detection and finality | v0.1 |
| `consumer` | The `Consumer` contract, `Run` and `Memory` | v0.1 |
| `internal/archtest` | Tests for the import rules, license headers and apply-path rules | v0.1 |
| `internal/fakechain` | A seeded fake chain and a fake `Source` for tests | v0.1 |
| `internal/invariant` | The reorg invariant property test | v0.1 |
| `rpc` | The JSON-RPC client that implements `chain.Source` | v0.2 |
| `ingest` | Backfill, realtime sync and validation | v0.2 |
| `evmsync` (root) | `App`, typed handler dispatch and the `Store` interface | v0.3 |
| `memstore` | An in-memory store with an undo journal | v0.3 |
| `cmd/evmsync` | The `evmsync gen` command | v0.3 |
| `internal/gen` | ABI to Go source generation | v0.3 |
| `pgstore` | The PostgreSQL store, a nested module | v0.4 |
| `e2e` | End-to-end tests against Anvil, a nested module | v0.2 |
| `examples` | Runnable examples, a nested module | v0.4 |

Which package may import which is fixed, and `internal/archtest` fails the tests when an import
breaks the rules. A new package needs a design change first: open an issue that proposes it. The
change that adds it also adds it to this table and to the inventory in `internal/archtest`.

## Rules for apply-path code

The apply path is the code that turns log entries into state: `chain`, `chainlog`, `tracker`,
`consumer` and `memstore`. Replaying the same log must always build the same state, so this code
follows four rules, and `internal/archtest` checks them. The dispatch code of the root `evmsync`
package and `pgstore` follows the same rules; review checks those by hand.

- **No wall clock.** Read time from the block (`chain.BlockRef.Time`), never from `time.Now`,
  `time.Since`, `time.Until`, `time.Sleep`, timers or tickers.
- **No global randomness.** No functions of `math/rand` or `math/rand/v2`.
- **No goroutines.** No `go` statement, except inside `consumer.Run`.
- **No map order.** Range over sorted keys, never over a map directly. Wrap `maps.Keys` and
  `maps.Values` in `slices.Sorted`; instead of `maps.All`, range over the sorted keys.

When the order of a map range never reaches state, an entry, an error or a log line, annotate the
loop with a reason:

```go
for _, v := range balances { //evmsync:maporder addition does not depend on order
	total += v
}
```

The directive applies to its own line when code precedes it, otherwise to the next line. It needs a
reason, it must suppress a finding, and it has no space after `//`.

## License header

Every `.go` file, generated files included, starts with these two lines and a blank line. Files
under `testdata/` are the exception.

```go
// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0
```

## Commits

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): a type, the
package as the scope, and a short subject, for example `feat(chainlog): add the record codec` or
`fix(tracker): stop at the finalized block`. Changes to `go.mod` and the lint config use the
`build` type, changes to workflows the `ci` type.
