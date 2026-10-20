// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"testing"

	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/consumer"
)

// nop is a consumer whose Apply only commits the offset.
type nop struct{ off chainlog.Offset }

func (n *nop) Offset(context.Context) (chainlog.Offset, error) { return n.off, nil }

func (n *nop) Apply(_ context.Context, e chainlog.Entry) error {
	n.off = e.Offset
	return nil
}

// BenchmarkRun runs a no-op consumer over a log of 100,000 block entries per iteration and
// reports the time per entry.
func BenchmarkRun(b *testing.B) {
	const entries = 100_000
	c, rs := refs(b, entries)
	es := chainEntries(c, rs, entries+1)[1:] // blocks only: blk(A1) … blk(A100000)
	log := newLog(b, es...)
	b.ReportAllocs()
	for b.Loop() {
		ctx, cancel := context.WithCancel(b.Context())
		if err := consumer.Run(ctx, log, &canceler{Consumer: &nop{}, at: entries, cancel: cancel}); err != context.Canceled {
			b.Fatalf("Run = %v, want context.Canceled", err)
		}
		cancel()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/entries, "ns/entry")
}
