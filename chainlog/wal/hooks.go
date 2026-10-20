// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"os"

	"github.com/hmdsefi/evmsync/chainlog"
)

// hooks let tests inject failures and observe the WAL's work. Open uses the zero hooks.
type hooks struct {
	write   func(f *os.File, p []byte) (int, error) // nil means f.Write
	replay  func(off chainlog.Offset)               // each entry Open applies after the checkpoint
	removed func(name string) bool                  // after each segment deletion; false stops compaction there
	sync    func(name string) error                 // before each fsync; an error replaces the fsync
	remove  func(path string) error                 // nil means os.Remove
}

func (h *hooks) writeTo(f *os.File, p []byte) (int, error) {
	if h.write != nil {
		return h.write(f, p)
	}
	return f.Write(p)
}

func (h *hooks) removeFile(path string) error {
	if h.remove != nil {
		return h.remove(path)
	}
	return os.Remove(path)
}
