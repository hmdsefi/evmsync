// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func BenchmarkFilterMatch(b *testing.B) {
	t2a, t2b := topA, topB
	f := Filter{
		Addresses: []common.Address{usdc},
		Topics:    [][]common.Hash{{transfer}, nil, {t2a, t2b}},
	}
	l := Log{Address: usdc, Topics: []common.Hash{transfer, topX, t2b}}
	if !f.Match(l) {
		b.Fatal("benchmark log does not match")
	}
	b.ReportAllocs()
	for b.Loop() {
		f.Match(l)
	}
}

func BenchmarkBloomTest(b *testing.B) {
	bl := bloomVector()
	b.ReportAllocs()
	for b.Loop() {
		bl.Test(transfer[:])
	}
}

func BenchmarkBloomMayContain(b *testing.B) {
	bl := bloomVector()
	f := Filter{Addresses: []common.Address{usdc}, Topics: [][]common.Hash{{transfer}}}
	b.ReportAllocs()
	for b.Loop() {
		bl.MayContain(f)
	}
}

func BenchmarkBloomOf(b *testing.B) {
	logs := make([]Log, 10)
	for i := range logs {
		logs[i] = Log{
			Address: usdc,
			Topics:  []common.Hash{transfer, one32, common.BytesToHash([]byte{byte(i)})},
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		BloomOf(logs)
	}
}

func TestPerf(t *testing.T) {
	assert := os.Getenv("EVMSYNC_PERF") == "1" && !raceEnabled
	tests := []struct {
		name      string
		bench     func(*testing.B)
		maxTime   time.Duration
		maxAllocs int64 // -1: not asserted
	}{
		{"FilterMatch", BenchmarkFilterMatch, 30 * time.Nanosecond, 0},
		{"BloomTest", BenchmarkBloomTest, time.Microsecond, 1},
		{"BloomMayContain", BenchmarkBloomMayContain, 2 * time.Microsecond, 2},
		{"BloomOf", BenchmarkBloomOf, 40 * time.Microsecond, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testing.Benchmark(tt.bench)
			if r.N == 0 {
				t.Fatal("benchmark did not run")
			}
			t.Logf("%d ns/op, %d allocs/op", r.NsPerOp(), r.AllocsPerOp())
			if !assert {
				return
			}
			if got := time.Duration(r.NsPerOp()); got > tt.maxTime {
				t.Errorf("%v/op, want at most %v", got, tt.maxTime)
			}
			if tt.maxAllocs >= 0 && r.AllocsPerOp() > tt.maxAllocs {
				t.Errorf("%d allocs/op, want at most %d", r.AllocsPerOp(), tt.maxAllocs)
			}
		})
	}
}
