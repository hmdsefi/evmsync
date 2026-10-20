// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"os"
	"testing"
	"time"
)

func TestPerf(t *testing.T) {
	if os.Getenv("EVMSYNC_PERF") != "1" || raceEnabled {
		t.Skip("set EVMSYNC_PERF=1 and run without -race to measure the targets")
	}
	tests := []struct {
		name  string
		bench func(*testing.B)
		check func(r testing.BenchmarkResult) string // "" when the target is met
	}{
		{"Append/never", func(b *testing.B) { benchAppend(b, SyncNever) }, func(r testing.BenchmarkResult) string {
			if d := time.Duration(r.NsPerOp()); d > 15*time.Microsecond {
				return d.String() + "/op, want at most 15µs"
			}
			return ""
		}},
		{"Read", BenchmarkRead, func(r testing.BenchmarkResult) string {
			if perSec := 256 * 1e9 / float64(r.NsPerOp()); perSec < 200_000 {
				return "fewer than 200,000 entries/s"
			}
			return ""
		}},
		{"Open", BenchmarkOpen, func(r testing.BenchmarkResult) string {
			if mibPerSec := float64(256<<20) / float64(r.NsPerOp()) * 1e9 / (1 << 20); mibPerSec < 150 {
				return "slower than 150 MiB/s"
			}
			return ""
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testing.Benchmark(tt.bench)
			if r.N == 0 {
				t.Fatal("benchmark did not run")
			}
			t.Logf("%d ns/op, %d allocs/op", r.NsPerOp(), r.AllocsPerOp())
			if msg := tt.check(r); msg != "" {
				t.Error(msg)
			}
		})
	}
}
