// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package archtest

// inventory lists the root module's package directories, as relative paths. v0.2 adds ingest and
// rpc; v0.3 adds "" (package evmsync), cmd/evmsync, internal/gen and memstore.
var inventory = []string{
	// v0.1
	"chain",
	"chainlog",
	"chainlog/logtest",
	"chainlog/wal",
	"consumer",
	"internal/archtest",
	"internal/fakechain",
	"internal/invariant",
	"tracker",
}
