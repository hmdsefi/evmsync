// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"testing"
)

func TestStreams(t *testing.T) {
	fnv := []struct {
		in   string
		want uint64
	}{
		{"", 0xcbf29ce484222325},
		{"chain", 0xa74390764c1a3056},
		{"logs", 0xce07d5ad70ec2c26},
		{"faults/stale", 0x69438d2139abd11a},
		{"invariant/params", 0xba8fbd86898fcb56},
	}
	for _, tt := range fnv {
		if got := fnv1a64(tt.in); got != tt.want {
			t.Errorf("fnv1a64(%q) = %#x, want %#x", tt.in, got, tt.want)
		}
	}

	streams := []struct {
		seed  uint64
		label string
		state uint64
		first [3]uint64
	}{
		{1, "chain", 0x3649bd9ac5186c97, [3]uint64{0xb47524943eba2e00, 0x16df773fd3f078b4, 0xeb395c59d379f2f6}},
		{1, "logs", 0x5f0df841f9ee70e7, [3]uint64{0x40f075f5f37e5d36, 0xc5c44323ac661942, 0xc9b55d5ceb77c672}},
		{0, "chain", 0x4563384f3707fdf9, [3]uint64{0xde2a3de9127ff783, 0x4f9ff908143d0f5f, 0xa6a22e8c8f1d2d63}},
		{0xffffffffffffffff, "invariant/params", 0x5e56ccf192eae776,
			[3]uint64{0xb3e78406c979e937, 0x0f23dcf4c65aca0b, 0xa85b690a296136ae}},
	}
	for _, tt := range streams {
		if got := streamState(tt.seed, tt.label); got != tt.state {
			t.Errorf("streamState(%#x, %q) = %#x, want %#x", tt.seed, tt.label, got, tt.state)
		}
		r := NewRand(tt.seed, tt.label)
		for i, want := range tt.first {
			if got := r.Uint64(); got != want {
				t.Errorf("NewRand(%#x, %q) output %d = %#x, want %#x", tt.seed, tt.label, i, got, want)
			}
		}
	}

	r := NewRand(1, "chain")
	if got := r.Intn(10); got != 7 {
		t.Errorf("Intn(10) = %d, want 7", got)
	}
	if got := r.Intn(1_000_000); got != 89347 {
		t.Errorf("Intn(1_000_000) = %d, want 89347", got)
	}
	if r.Chance(500_000) {
		t.Error("Chance(500_000) = true, want false")
	}

	r = NewRand(7, "logs")
	want := []uint64{2, 2, 1, 1, 1}
	for i, w := range want {
		if got := r.Intn(4); got != w {
			t.Errorf("NewRand(7, \"logs\") Intn(4) #%d = %d, want %d", i, got, w)
		}
	}

	wantPanic(t, "evmsync: fakechain: Intn(0)", func() { NewRand(1, "chain").Intn(0) })
}

func TestChanceDrawsOnce(t *testing.T) {
	for _, ppm := range []uint32{0, 1, 500_000, 1_000_000, 2_000_000} {
		r, twin := NewRand(5, "chain"), NewRand(5, "chain")
		got := r.Chance(ppm)
		want := twin.Intn(1_000_000) < uint64(ppm)
		if got != want {
			t.Errorf("Chance(%d) = %v, want %v", ppm, got, want)
		}
		if r.Uint64() != twin.Uint64() {
			t.Errorf("Chance(%d) did not draw exactly once", ppm)
		}
	}
	v := uint32(NewRand(5, "chain").Intn(1_000_000)) //nolint:gosec // below 1,000,000
	if NewRand(5, "chain").Chance(v) || !NewRand(5, "chain").Chance(v+1) {
		t.Errorf("Chance(ppm) for a draw of %d: want false at ppm %d and true at %d", v, v, v+1)
	}
	if !NewRand(5, "chain").Chance(1_000_000) {
		t.Error("Chance(1_000_000) = false, want true")
	}
	if NewRand(5, "chain").Chance(0) {
		t.Error("Chance(0) = true, want false")
	}
}

// wantPanic fails t unless fn panics with exactly msg.
func wantPanic(t *testing.T, msg string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Errorf("no panic, want %q", msg)
			return
		}
		got, ok := r.(string)
		if !ok {
			err, isErr := r.(error)
			if !isErr {
				t.Errorf("panic value %v (%T), want string %q", r, r, msg)
				return
			}
			got = err.Error()
		}
		if got != msg {
			t.Errorf("panic %q, want %q", got, msg)
		}
	}()
	fn()
}
