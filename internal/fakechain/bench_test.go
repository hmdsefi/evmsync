// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"testing"
)

// BenchmarkStep measures one Chain.Step with default parameters; the target is 20 µs/op.
func BenchmarkStep(b *testing.B) {
	c := New(1, Params{})
	b.ReportAllocs()
	for b.Loop() {
		c.Step()
	}
}
