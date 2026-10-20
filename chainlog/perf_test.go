// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

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
		name      string
		bench     func(*testing.B)
		maxTime   time.Duration
		maxAllocs int64 // -1: not asserted
	}{
		{"Encode", BenchmarkEncode, time.Microsecond, 0},
		{"Decode", BenchmarkDecode, 2 * time.Microsecond, 3},
		{"StateBlock", BenchmarkStateBlock, 300 * time.Nanosecond, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testing.Benchmark(tt.bench)
			if r.N == 0 {
				t.Fatal("benchmark did not run")
			}
			t.Logf("%d ns/op, %d allocs/op", r.NsPerOp(), r.AllocsPerOp())
			if got := time.Duration(r.NsPerOp()); got > tt.maxTime {
				t.Errorf("%v/op, want at most %v", got, tt.maxTime)
			}
			if tt.maxAllocs >= 0 && r.AllocsPerOp() > tt.maxAllocs {
				t.Errorf("%d allocs/op, want at most %d", r.AllocsPerOp(), tt.maxAllocs)
			}
		})
	}
}
