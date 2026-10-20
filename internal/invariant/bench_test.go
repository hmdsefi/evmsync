// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"testing"

	"github.com/hmdsefi/evmsync/internal/fakechain"
)

// fakechainParams returns chain parameters with the given fork chance and depth, defaults
// otherwise.
func fakechainParams(forkPPM uint32, maxDepth uint64) fakechain.Params {
	return fakechain.Params{ForkPPM: forkPPM, MaxForkDepth: maxDepth}
}

// BenchmarkScenario runs one 200-step scenario with the memory subject per iteration, over seeds
// 1 to 100 in turn; the target is 5 ms per scenario on CI amd64.
func BenchmarkScenario(b *testing.B) {
	cfg := memoryConfig()
	seed := uint64(1)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Check(b, cfg, seed); err != nil {
			b.Fatalf("seed %d: %v", seed, err)
		}
		seed = seed%100 + 1
	}
}
