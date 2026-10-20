// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"math/bits"
)

// Rand is a SplitMix64 stream. Its outputs depend only on the seed and the label it was created
// with, never on the Go version or the platform. It is not safe for concurrent use.
type Rand struct {
	state uint64
}

// NewRand returns the stream for label under seed. Each user of randomness has its own label, so
// adding a draw to one stream never shifts another.
func NewRand(seed uint64, label string) *Rand {
	return &Rand{state: streamState(seed, label)}
}

// Uint64 returns the next output.
func (r *Rand) Uint64() uint64 {
	return splitMix64(&r.state)
}

// Intn returns a value in [0, n) from exactly one Uint64 draw, by multiply-shift. It panics if n
// is 0.
func (r *Rand) Intn(n uint64) uint64 {
	if n == 0 {
		panic("evmsync: fakechain: Intn(0)")
	}
	hi, _ := bits.Mul64(r.Uint64(), n)
	return hi
}

// Chance reports true with probability ppm/1,000,000 from exactly one Intn draw, also when ppm
// is 0 or at least 1,000,000.
func (r *Rand) Chance(ppm uint32) bool {
	return r.Intn(1_000_000) < uint64(ppm)
}

func fnv1a64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func splitMix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// streamState is the initial state of stream label under seed.
func streamState(seed uint64, label string) uint64 {
	st := seed
	k := splitMix64(&st)
	return k ^ fnv1a64(label)
}
