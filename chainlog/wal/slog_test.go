// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"context"
	"log/slog"
	"sync"
)

// recordHandler keeps every record logged through it.
type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func newRecorder() (*slog.Logger, *recordHandler) {
	h := &recordHandler{}
	return slog.New(h), h
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordHandler) WithGroup(string) slog.Handler { return h }

// find returns the attributes of the last record with level and msg.
func (h *recordHandler) find(level slog.Level, msg string) (map[string]any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.records) - 1; i >= 0; i-- {
		r := h.records[i]
		if r.Level != level || r.Message != msg {
			continue
		}
		attrs := make(map[string]any)
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		return attrs, true
	}
	return nil, false
}
