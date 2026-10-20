// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"testing"

	"github.com/hmdsefi/evmsync/internal/fakechain"
)

func TestVariantFor(t *testing.T) {
	tests := []struct {
		seed uint64
		want Variant
	}{
		{1, Variant{
			Params: fakechain.Params{ForkPPM: 150_000, MaxForkDepth: 1, MaxMine: 6},
			Faults: fakechain.Faults{DropOrphans: true},
		}},
		{2, Variant{
			Params: fakechain.Params{ForkPPM: 400_000, MaxForkDepth: 3, MaxMine: 6},
			Faults: fakechain.Faults{FinalizedUnsupported: true, StaleHeadPPM: 100_000, StaleOrphanPPM: 100_000,
				DropOrphans: true},
		}},
	}
	for _, tt := range tests {
		if got := VariantFor(tt.seed); got != tt.want {
			t.Errorf("VariantFor(%d) = %+v, want %+v", tt.seed, got, tt.want)
		}
	}

	// Every choice of every field appears over the first 200 seeds, and nothing else does.
	seen := map[string]map[any]bool{}
	note := func(field string, v any) {
		if seen[field] == nil {
			seen[field] = map[any]bool{}
		}
		seen[field][v] = true
	}
	for seed := uint64(1); seed <= 200; seed++ {
		v := VariantFor(seed)
		note("ForkPPM", v.Params.ForkPPM)
		note("MaxForkDepth", v.Params.MaxForkDepth)
		note("MaxMine", v.Params.MaxMine)
		note("FinalizedUnsupported", v.Faults.FinalizedUnsupported)
		note("StaleHeadPPM", v.Faults.StaleHeadPPM)
		note("StaleOrphanPPM", v.Faults.StaleOrphanPPM)
		note("DropOrphans", v.Faults.DropOrphans)
		note("InterleavePPM", v.Faults.InterleavePPM)
		rest := v
		rest.Params.ForkPPM, rest.Params.MaxForkDepth, rest.Params.MaxMine = 0, 0, 0
		rest.Faults.FinalizedUnsupported, rest.Faults.StaleHeadPPM, rest.Faults.StaleOrphanPPM = false, 0, 0
		rest.Faults.DropOrphans, rest.Faults.InterleavePPM = false, 0
		if rest != (Variant{}) {
			t.Fatalf("VariantFor(%d) sets other fields: %+v", seed, v)
		}
	}
	want := map[string]int{
		"ForkPPM": 3, "MaxForkDepth": 3, "MaxMine": 3, "FinalizedUnsupported": 2,
		"StaleHeadPPM": 2, "StaleOrphanPPM": 2, "DropOrphans": 2, "InterleavePPM": 3,
	}
	for field, n := range want {
		if len(seen[field]) != n {
			t.Errorf("%s took values %v over 200 seeds, want %d distinct", field, seen[field], n)
		}
	}
	for _, v := range []any{uint32(50_000), uint32(150_000), uint32(400_000)} {
		if !seen["ForkPPM"][v] {
			t.Errorf("ForkPPM never %v", v)
		}
	}
	for _, v := range []any{uint64(1), uint64(3), uint64(8)} {
		if !seen["MaxForkDepth"][v] {
			t.Errorf("MaxForkDepth never %v", v)
		}
	}
	for _, v := range []any{uint64(1), uint64(3), uint64(6)} {
		if !seen["MaxMine"][v] {
			t.Errorf("MaxMine never %v", v)
		}
	}
	for _, v := range []any{uint32(0), uint32(50_000), uint32(100_000)} {
		if !seen["InterleavePPM"][v] {
			t.Errorf("InterleavePPM never %v", v)
		}
	}
	if !seen["StaleHeadPPM"][uint32(100_000)] || !seen["StaleOrphanPPM"][uint32(100_000)] {
		t.Error("a stale fault never took 100,000")
	}
}

// TestVariantDraws checks VariantFor's draw order on the "invariant/params" stream against a model.
func TestVariantDraws(t *testing.T) {
	for seed := uint64(1); seed <= 100; seed++ {
		r := fakechain.NewRand(seed, "invariant/params")
		var want Variant
		want.Params.ForkPPM = []uint32{50_000, 150_000, 400_000}[r.Intn(3)]
		want.Params.MaxForkDepth = []uint64{1, 3, 8}[r.Intn(3)]
		want.Params.MaxMine = []uint64{1, 3, 6}[r.Intn(3)]
		want.Faults.FinalizedUnsupported = r.Intn(1_000_000) < 250_000
		want.Faults.StaleHeadPPM = []uint32{0, 100_000}[r.Intn(2)]
		want.Faults.StaleOrphanPPM = []uint32{0, 100_000}[r.Intn(2)]
		want.Faults.DropOrphans = r.Intn(1_000_000) < 500_000
		want.Faults.InterleavePPM = []uint32{0, 50_000, 100_000}[r.Intn(3)]
		if got := VariantFor(seed); got != want {
			t.Fatalf("VariantFor(%d) = %+v, want %+v", seed, got, want)
		}
	}
}
