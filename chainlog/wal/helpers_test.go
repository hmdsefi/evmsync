// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/chainlog/logtest"
)

// linkedBlocks returns n linked blocks first..first+n-1 of chain 0 (hashes as in logtest), each
// with one log of one topic and dataLen bytes of data.
func linkedBlocks(first, n uint64, dataLen int) []chainlog.Entry {
	es := make([]chainlog.Entry, 0, n)
	for num := first; num < first+n; num++ {
		r := ref(num)
		l := chain.Log{BlockNumber: r.Number, BlockHash: r.Hash, TxHash: r.Hash, Index: 1}
		l.Address[19] = 0xaa
		l.Topics = append(l.Topics, r.Parent)
		if dataLen > 0 {
			l.Data = make([]byte, dataLen)
			l.Data[0] = 0x5a
		}
		es = append(es, chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r, Logs: []chain.Log{l}}})
	}
	return es
}

func finalizeTo(r chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindFinalize, To: r}
}

func revertTo(r chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindRevert, To: r}
}

// testOpts are the options of most tests: the conformance suite's meta and cap, no fsync.
func testOpts(segmentSize int64) Options {
	return Options{Meta: logtest.Meta(), WindowCap: logtest.WindowCap, Sync: SyncNever, SegmentSize: segmentSize}
}

// openLog opens dir with opts and h and closes the log when the test ends.
func openLog(t *testing.T, dir string, opts Options, h hooks) *Log {
	t.Helper()
	l, err := openWithHooks(dir, opts, h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func mustAppend(t *testing.T, l *Log, es ...chainlog.Entry) chainlog.Offset {
	t.Helper()
	off, err := l.Append(t.Context(), es...)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return off
}

func mustClose(t *testing.T, l *Log) {
	t.Helper()
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// snapshotOf is what a log shows: Head, Oldest, the state's accessors and every retained entry.
type snapshotOf struct {
	head, oldest chainlog.Offset
	state        stateView
	entries      []chainlog.Entry
}

func snap(t *testing.T, l chainlog.Log) snapshotOf {
	t.Helper()
	ctx := t.Context()
	var s snapshotOf
	var err error
	if s.head, err = l.Head(ctx); err != nil {
		t.Fatalf("Head: %v", err)
	}
	if s.oldest, err = l.Oldest(ctx); err != nil {
		t.Fatalf("Oldest: %v", err)
	}
	st, err := l.State(ctx)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	s.state = viewOf(st)
	if s.entries, err = l.Read(ctx, s.oldest, 1<<20); err != nil {
		t.Fatalf("Read: %v", err)
	}
	return s
}

func wantSame(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s =\n%+v\nwant\n%+v", what, got, want)
	}
}

// listing returns the names and sizes of the files in dir.
func listing(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	m := make(map[string]int64, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		m[e.Name()] = fi.Size()
	}
	return m
}

// copyDir copies the files of src into a new directory and returns it.
func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	for name := range maps.Keys(listing(t, src)) {
		in, err := os.Open(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		out, err := os.OpenFile(filepath.Join(dst, name), os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatalf("Copy: %v", err)
		}
		_ = in.Close()
		if err := out.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	return dst
}

// segmentNames returns the segment files of dir in offset order.
func segmentNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if _, ok := parseSegmentName(e.Name()); ok {
			names = append(names, e.Name())
		}
	}
	return names
}
