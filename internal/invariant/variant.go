// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"github.com/hmdsefi/evmsync/internal/fakechain"
)

// Filter is the ingest filter of every scenario: fakechain.DefaultFilter.
var Filter = fakechain.DefaultFilter

// FallbackDepth is the finality depth the harness uses when the source has no finalized tag.
const FallbackDepth = 64

// Variant is a scenario's chain parameters and provider faults.
type Variant struct {
	Params fakechain.Params
	Faults fakechain.Faults
}

// VariantFor draws seed's variant from the seed's "invariant/params" stream: the fork chance and
// depth, the blocks per mining step, and the provider faults the tracker must survive. The other
// fields are zero, so they take their defaults. Logs are never dropped: the tracker does not
// validate logs, ingest does.
func VariantFor(seed uint64) Variant {
	r := fakechain.NewRand(seed, "invariant/params")
	var v Variant
	v.Params.ForkPPM = [3]uint32{50_000, 150_000, 400_000}[r.Intn(3)]
	v.Params.MaxForkDepth = [3]uint64{1, 3, 8}[r.Intn(3)]
	v.Params.MaxMine = [3]uint64{1, 3, 6}[r.Intn(3)]
	v.Faults.FinalizedUnsupported = r.Chance(250_000)
	v.Faults.StaleHeadPPM = [2]uint32{0, 100_000}[r.Intn(2)]
	v.Faults.StaleOrphanPPM = [2]uint32{0, 100_000}[r.Intn(2)]
	v.Faults.DropOrphans = r.Chance(500_000)
	v.Faults.InterleavePPM = [3]uint32{0, 50_000, 100_000}[r.Intn(3)]
	return v
}
