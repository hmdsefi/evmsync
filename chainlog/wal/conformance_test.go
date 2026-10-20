// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal_test

import (
	"testing"

	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/chainlog/logtest"
	"github.com/hmdsefi/evmsync/chainlog/wal"
)

func TestConformance(t *testing.T) {
	tests := []struct {
		name        string
		sync        wal.SyncPolicy
		segmentSize int64
	}{
		{"never-0", wal.SyncNever, 0},
		{"never-256", wal.SyncNever, 256},
		{"always-4096", wal.SyncAlways, 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logtest.Run(t, func(t *testing.T) chainlog.Log {
				l, err := wal.Open(t.TempDir(), wal.Options{
					Meta: logtest.Meta(), WindowCap: logtest.WindowCap, Sync: tt.sync, SegmentSize: tt.segmentSize,
				})
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				return l
			})
		})
	}
}
