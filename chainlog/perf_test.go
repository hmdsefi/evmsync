// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"os"
	"testing"
	"time"
)

func TestPerf(t *testing.T) {
	assert := os.Getenv("EVMSYNC_PERF") == "1" && !raceEnabled
	tests := []struct {
		name      string
		bench     func(*testing.B)
		maxTime   time.Duration
		maxAllocs int64 // -1: not asserted
	}{
		{"Encode", BenchmarkEncode, time.Microsecond, 0},
		{"Decode", BenchmarkDecode, 2 * time.Microsecond, 3},
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
