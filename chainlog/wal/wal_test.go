// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/chainlog/logtest"
)

const firstSegment = "00000000000000000001.seg"

func wantCorrupt(t *testing.T, err error, text string) {
	t.Helper()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open error = %v, want one wrapping ErrCorrupt", err)
	}
	if got := err.Error(); got != text {
		t.Errorf("Open error =\n%q\nwant\n%q", got, text)
	}
}

func TestOpenOptions(t *testing.T) {
	tests := []struct {
		opts Options
		want string
	}{
		{Options{SegmentSize: -1}, "wal: invalid options: segment size -1 is negative"},
		{Options{Sync: 2}, "wal: invalid options: unknown sync policy 2"},
		{Options{WindowCap: -1}, "wal: invalid options: window cap -1 is negative"},
	}
	for _, tt := range tests {
		dir := filepath.Join(t.TempDir(), "wal")
		_, err := Open(dir, tt.opts)
		if err == nil || err.Error() != tt.want {
			t.Errorf("Open(%+v) = %v, want %q", tt.opts, err, tt.want)
		}
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open with bad options touched the directory: %v", err)
		}
	}

	dir := filepath.Join(t.TempDir(), "wal")
	var five chainlog.Meta
	for range 5 {
		five.Filter.Topics = append(five.Filter.Topics, nil)
	}
	_, err := Open(dir, Options{Meta: five})
	if !errors.Is(err, chain.ErrInvalidFilter) {
		t.Fatalf("Open with a 5-position filter = %v, want one wrapping chain.ErrInvalidFilter", err)
	}
	if got, want := err.Error(), "wal: invalid options: meta: chain: invalid filter: 5 topic positions, at most 4"; got != want {
		t.Errorf("Open error = %q, want %q", got, want)
	}
	if got := slices.Sorted(maps.Keys(listing(t, dir))); !slices.Equal(got, []string{"lock"}) {
		t.Errorf("directory holds %v, want only lock", got)
	}
	// The directory is still new: a valid meta creates the log.
	l := openLog(t, dir, testOpts(0), hooks{})
	if h, _ := l.Head(t.Context()); h != 0 {
		t.Errorf("Head = %d, want 0", h)
	}
}

func TestNewDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	opts := Options{Sync: SyncAlways} // zero meta: a random ID
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := listing(t, dir); !maps.Equal(got, map[string]int64{"lock": 0, "meta": got["meta"], firstSegment: 0}) {
		t.Errorf("new directory holds %v, want lock, meta and an empty %s", got, firstSegment)
	}
	for _, name := range []string{"lock", "meta", firstSegment} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %v, want 0600", name, mode)
		}
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o750 {
		t.Errorf("directory has mode %v, want 0750", fi.Mode().Perm())
	}
	m, err := l.Meta(t.Context())
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if m.ID.IsZero() {
		t.Error("new log has a zero ID, want a random one")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "meta"))
	if string(b[8:16]) != "EVMSMETA" || b[16] != 0x01 || !bytes.Equal(b[17:33], m.ID[:]) {
		t.Errorf("meta starts %x, want the header, EVMSMETA, 01 and the ID", b[:33])
	}
	mustClose(t, l)

	// An existing directory keeps its meta and ignores Options.Meta; Open removes stray
	// temporary files and keeps files it does not own.
	for _, name := range []string{"meta.tmp", "checkpoint.tmp", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l = openLog(t, dir, testOpts(0), hooks{})
	again, _ := l.Meta(t.Context())
	wantSame(t, "Meta after reopening with another Options.Meta", again, m)
	got := listing(t, dir)
	if _, ok := got["meta.tmp"]; ok {
		t.Error("Open kept meta.tmp")
	}
	if _, ok := got["checkpoint.tmp"]; ok {
		t.Error("Open kept checkpoint.tmp")
	}
	if _, ok := got["notes.txt"]; !ok {
		t.Error("Open removed notes.txt")
	}
}

func TestOpenAfterInterruptedCreate(t *testing.T) {
	dir := t.TempDir()
	meta := logtest.Meta()
	if err := os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta"), encodeMeta(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := testOpts(0)
	opts.Meta.ID = chainlog.ID{0xee}
	l := openLog(t, dir, opts, hooks{})
	if h, _ := l.Head(t.Context()); h != 0 {
		t.Errorf("Head = %d, want 0", h)
	}
	m, _ := l.Meta(t.Context())
	wantSame(t, "Meta", m, meta)
	if size, ok := listing(t, dir)[firstSegment]; !ok || size != 0 {
		t.Errorf("Open did not create an empty %s", firstSegment)
	}
	mustAppend(t, l, linkedBlocks(1, 2, 8)...)
}

// newLog creates a log in a new directory with n linked blocks and closes it.
func newLog(t *testing.T, opts Options, n uint64) string {
	t.Helper()
	dir := t.TempDir()
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if n > 0 {
		mustAppend(t, l, linkedBlocks(1, n, 40)...)
	}
	mustClose(t, l)
	return dir
}

// rewrite replaces dir/name with f applied to its bytes.
func rewrite(t *testing.T, dir, name string, f func([]byte) []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f(bytes.Clone(b)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// openCorrupt opens dir, wants ErrCorrupt with text, and checks that no file changed.
func openCorrupt(t *testing.T, dir, text string) {
	t.Helper()
	before := listing(t, dir)
	l, err := Open(dir, testOpts(0))
	if err == nil {
		_ = l.Close()
	}
	wantCorrupt(t, err, text)
	wantSame(t, "files after a failed Open", listing(t, dir), before)
}

func TestMetaCorruptOpen(t *testing.T) {
	base := newLog(t, testOpts(0), 3)
	m := logtest.Meta()
	head := cat([]byte("EVMSMETA"), []byte{0x01}, m.ID[:], uv(m.ChainID), uv(m.Start))
	tests := []struct {
		name   string
		damage func(dir string)
		detail string
	}{
		{"missing", func(dir string) { _ = os.Remove(filepath.Join(dir, "meta")) }, "missing"},
		{"cut to 5 bytes", func(dir string) { rewrite(t, dir, "meta", func(b []byte) []byte { return b[:5] }) }, "truncated"},
		{"flipped body byte", func(dir string) {
			rewrite(t, dir, "meta", func(b []byte) []byte { b[20] ^= 1; return b })
		}, "checksum mismatch"},
		{"bad magic", func(dir string) {
			rewrite(t, dir, "meta", func(b []byte) []byte { return seal(cat([]byte("XXXXXXXX"), b[16:])) })
		}, "bad magic"},
		{"version 2", func(dir string) {
			rewrite(t, dir, "meta", func(b []byte) []byte { return seal(cat(b[8:16], []byte{0x02}, b[17:])) })
		}, "version 2"},
		{"npos 5", func(dir string) {
			rewrite(t, dir, "meta", func([]byte) []byte { return seal(cat(head, uv(0), uv(5))) })
		}, "filter out of range"},
		{"trailing byte", func(dir string) {
			rewrite(t, dir, "meta", func(b []byte) []byte { return seal(cat(b[8:], []byte{0x00})) })
		}, "trailing bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := copyDir(t, base)
			tt.damage(dir)
			openCorrupt(t, dir, "wal: corrupt log: meta: "+tt.detail)
		})
	}
}

func TestPersistence(t *testing.T) {
	ctx := t.Context()
	in := linkedBlocks(1, 20, 16)
	in = append(in, finalizeTo(ref(10)), revertTo(ref(15)))
	in = append(in, linkedBlocks(16, 25, 16)...)
	in = append(in, finalizeTo(ref(30)))
	in = append(in, linkedBlocks(41, 2, 16)...)
	if len(in) != 50 {
		t.Fatalf("test setup: %d entries, want 50", len(in))
	}
	opts := testOpts(512)
	opts.WindowCap = 0
	dir := t.TempDir()
	l := openLog(t, dir, opts, hooks{})
	for _, e := range in {
		mustAppend(t, l, e)
	}
	before := snap(t, l)
	meta, _ := l.Meta(ctx)
	mustClose(t, l)

	mem := chainlog.NewMemory(opts.Meta, 0)
	for _, e := range in {
		if _, err := mem.Append(ctx, e); err != nil {
			t.Fatalf("Memory.Append: %v", err)
		}
	}
	wantSame(t, "WAL before close vs Memory", before, snap(t, mem))

	l = openLog(t, dir, opts, hooks{})
	wantSame(t, "WAL after reopen", snap(t, l), before)
	m, _ := l.Meta(ctx)
	wantSame(t, "Meta after reopen", m, meta)
	if off := mustAppend(t, l, linkedBlocks(43, 1, 16)...); off != 51 {
		t.Errorf("Append after reopen returned %d, want 51", off)
	}
}

func TestRollAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, testOpts(512), hooks{})
	next := uint64(1)
	for len(segmentNames(t, dir)) < 3 {
		mustAppend(t, l, linkedBlocks(next, 1, 100)...)
		next++
	}
	// A batch larger than the segment size gets a segment of its own.
	big := linkedBlocks(next, 1, 600)
	if n := len(chainlog.Encode(nil, chainlog.Entry{Offset: 1, Kind: chainlog.KindBlock, Block: big[0].Block})); n <= 512 {
		t.Fatalf("test setup: big record is %d bytes, want more than 512", n)
	}
	bigOff := mustAppend(t, l, big...)
	mustAppend(t, l, linkedBlocks(next+1, 1, 100)...)
	head, _ := l.Head(t.Context())
	before := snap(t, l)
	mustClose(t, l)

	names := segmentNames(t, dir)
	sizes := listing(t, dir)
	for i, name := range names {
		first, _ := parseSegmentName(name)
		if i > 0 {
			prev, _ := parseSegmentName(names[i-1])
			if first <= prev {
				t.Errorf("segment %s does not follow %s", name, names[i-1])
			}
		}
		if i < len(names)-1 && sizes[name] > 512 && first != bigOff {
			t.Errorf("sealed segment %s holds %d bytes, more than 512", name, sizes[name])
		}
	}
	bigName := segmentName(bigOff)
	if sizes[bigName] <= 512 {
		t.Errorf("the big batch's segment %s holds %d bytes, want more than 512", bigName, sizes[bigName])
	}
	if next, _ := parseSegmentName(names[slices.Index(names, bigName)+1]); next != bigOff+1 {
		t.Errorf("the big batch's segment holds more than its batch: next segment starts at %d", next)
	}

	b, err := os.ReadFile(filepath.Join(dir, "checkpoint"))
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	cp, err := decodeCheckpoint(b, 0)
	if err != nil {
		t.Fatalf("decodeCheckpoint: %v", err)
	}
	lastFirst, _ := parseSegmentName(names[len(names)-1])
	if cp.x != lastFirst-1 {
		t.Errorf("checkpoint at %d, want the head at the last roll, %d", cp.x, lastFirst-1)
	}

	var replayed []chainlog.Offset
	l = openLog(t, dir, testOpts(512), hooks{replay: func(off chainlog.Offset) { replayed = append(replayed, off) }})
	var want []chainlog.Offset
	for off := cp.x + 1; off <= head; off++ {
		want = append(want, off)
	}
	wantSame(t, "replayed offsets", replayed, want)
	wantSame(t, "log after reopen", snap(t, l), before)
}

// damageCheckpoint rewrites the checkpoint file of dir.
func damageCheckpoint(t *testing.T, dir string, f func([]byte) []byte) {
	t.Helper()
	rewrite(t, dir, checkpointName, f)
}

func TestCheckpointOpen(t *testing.T) {
	base := t.TempDir()
	l := openLog(t, base, testOpts(512), hooks{})
	for n := uint64(1); len(segmentNames(t, base)) < 3; n++ {
		mustAppend(t, l, linkedBlocks(n, 1, 100)...)
	}
	head, _ := l.Head(t.Context())
	mustClose(t, l)
	b, _ := os.ReadFile(filepath.Join(base, checkpointName))
	cp, err := decodeCheckpoint(b, 0)
	if err != nil {
		t.Fatalf("decodeCheckpoint: %v", err)
	}
	stateBin, _ := cp.state.MarshalBinary()
	reencode := func(x, l uint64, state []byte) func([]byte) []byte {
		return func([]byte) []byte { return seal(cat([]byte("EVMSCKPT"), []byte{0x01}, uv(x), uv(l), state)) }
	}
	badState := bytes.Clone(stateBin)
	badState[0] = 2
	names := segmentNames(t, base)
	f1, _ := parseSegmentName(names[1])

	tests := []struct {
		name   string
		damage func(dir string)
		text   string
	}{
		{"cut", func(dir string) { damageCheckpoint(t, dir, func(b []byte) []byte { return b[:len(b)-1] }) }, "checkpoint: truncated"},
		{"flipped byte", func(dir string) {
			damageCheckpoint(t, dir, func(b []byte) []byte { b[len(b)-1] ^= 1; return b })
		}, "checkpoint: checksum mismatch"},
		{"bad magic", func(dir string) {
			damageCheckpoint(t, dir, func(b []byte) []byte { return seal(cat([]byte("XXXXXXXX"), b[16:])) })
		}, "checkpoint: bad magic"},
		{"version 2", func(dir string) {
			damageCheckpoint(t, dir, func(b []byte) []byte { return seal(cat(b[8:16], []byte{0x02}, b[17:])) })
		}, "checkpoint: version 2"},
		{"l above x", func(dir string) { damageCheckpoint(t, dir, reencode(uint64(cp.x), uint64(cp.x)+1, stateBin)) }, "checkpoint: bad header"},
		{"bad state", func(dir string) { damageCheckpoint(t, dir, reencode(uint64(cp.x), 0, badState)) }, "checkpoint: chainlog: bad state encoding: version 2"},
		{"x above head", func(dir string) { damageCheckpoint(t, dir, reencode(uint64(head)+5, 0, stateBin)) },
			"checkpoint at " + strconv.FormatUint(uint64(head)+5, 10) + " outside [0, " + strconv.FormatUint(uint64(head), 10) + "]"},
		{"no checkpoint, first segment not 1", func(dir string) {
			_ = os.Remove(filepath.Join(dir, checkpointName))
			_ = os.Remove(filepath.Join(dir, names[0]))
		}, "first segment starts at " + strconv.FormatUint(uint64(f1), 10) + " without a checkpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := copyDir(t, base)
			tt.damage(dir)
			openCorrupt(t, dir, "wal: corrupt log: "+tt.text)
		})
	}
}

func TestOpenScanErrors(t *testing.T) {
	base := t.TempDir()
	l := openLog(t, base, testOpts(512), hooks{})
	for n := uint64(1); len(segmentNames(t, base)) < 3; n++ {
		mustAppend(t, l, linkedBlocks(n, 1, 100)...)
	}
	mustClose(t, l)
	names := segmentNames(t, base)
	first1, _ := parseSegmentName(names[1])
	last0 := first1 - 1

	tests := []struct {
		name   string
		damage func(dir string)
		text   string
	}{
		{"no segment", func(dir string) {
			for _, n := range names {
				_ = os.Remove(filepath.Join(dir, n))
			}
		}, "no segment"},
		{"gap", func(dir string) {
			_ = os.Rename(filepath.Join(dir, names[1]), filepath.Join(dir, segmentName(first1+1)))
		}, "segment " + segmentName(first1+1) + " does not follow offset " + strconv.FormatUint(uint64(last0), 10)},
		{"empty sealed segment", func(dir string) {
			_ = os.Truncate(filepath.Join(dir, names[1]), 0)
		}, "segment " + names[1] + " is empty"},
		{"sealed segment cut inside its record", func(dir string) {
			rewrite(t, dir, names[0], func(b []byte) []byte { return b[:len(b)-5] })
		}, "segment " + names[0] + " position 0: chainlog: incomplete record"},
		{"sealed segment with a length above the limit", func(dir string) {
			rewrite(t, dir, names[0], func(b []byte) []byte { b[0], b[1], b[2], b[3] = 0xff, 0xff, 0xff, 0xff; return b })
		}, "segment " + names[0] + " position 0: chainlog: record too large"},
		{"flipped byte in the first segment", func(dir string) {
			rewrite(t, dir, names[0], func(b []byte) []byte { b[20] ^= 1; return b })
		}, "segment " + names[0] + " position 0: chainlog: record checksum mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := copyDir(t, base)
			tt.damage(dir)
			before := listing(t, dir)
			_, err := Open(dir, testOpts(512))
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Open = %v, want one wrapping ErrCorrupt", err)
			}
			if want := "wal: corrupt log: " + tt.text; !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Open error =\n%q\nwant the prefix\n%q", err, want)
			}
			wantSame(t, "files after a failed Open", listing(t, dir), before)
		})
	}
}

// failingWrite returns a write hook that fails with syscall.EIO while *fail is set.
func failingWrite(fail *bool) func(f *os.File, p []byte) (int, error) {
	return func(f *os.File, p []byte) (int, error) {
		if *fail {
			return 0, syscall.EIO
		}
		return f.Write(p)
	}
}

func TestWriteFailure(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	var fail bool
	l := openLog(t, dir, testOpts(0), hooks{write: failingWrite(&fail)})
	mustAppend(t, l, linkedBlocks(1, 3, 8)...)
	fail = true
	_, err := l.Append(ctx, linkedBlocks(4, 1, 8)...)
	if !errors.Is(err, ErrFailed) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("Append with a failing write = %v, want one wrapping ErrFailed and EIO", err)
	}
	if got, want := err.Error(), "wal: log failed: append: "+syscall.EIO.Error(); got != want {
		t.Errorf("Append error = %q, want %q", got, want)
	}
	if h, _ := l.Head(ctx); h != 3 {
		t.Errorf("Head = %d after the failed Append, want 3", h)
	}
	fail = false // the failure is sticky even when writes would work again
	earlier := "wal: log failed: earlier error: " + syscall.EIO.Error()
	if _, err := l.Append(ctx, linkedBlocks(4, 1, 8)...); err == nil || err.Error() != earlier || !errors.Is(err, ErrFailed) {
		t.Errorf("next Append = %v, want %q", err, earlier)
	}
	if err := l.Truncate(ctx, 1); err == nil || err.Error() != earlier {
		t.Errorf("Truncate = %v, want %q", err, earlier)
	}
	if es, err := l.Read(ctx, 1, 10); err != nil || len(es) != 3 {
		t.Errorf("Read after the failure = %d entries, %v; want 3, nil", len(es), err)
	}
	mustClose(t, l)

	l = openLog(t, dir, testOpts(0), hooks{})
	if h, _ := l.Head(ctx); h != 3 {
		t.Errorf("Head after reopen = %d, want 3", h)
	}
	if off := mustAppend(t, l, linkedBlocks(4, 1, 8)...); off != 4 {
		t.Errorf("Append after reopen returned %d, want 4", off)
	}
}

func TestReadCorrupt(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, testOpts(0), hooks{})
	es := linkedBlocks(1, 3, 40)
	mustAppend(t, l, es...)
	rec1 := len(chainlog.Encode(nil, chainlog.Entry{Offset: 1, Kind: es[0].Kind, Block: es[0].Block}))
	f, err := os.OpenFile(filepath.Join(dir, firstSegment), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xff}, int64(rec1+30)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_, err = l.Read(t.Context(), 2, 1)
	if !errors.Is(err, ErrCorrupt) || !errors.Is(err, chainlog.ErrChecksum) {
		t.Fatalf("Read of a damaged record = %v, want one wrapping ErrCorrupt and chainlog.ErrChecksum", err)
	}
	if want := "wal: corrupt log: segment " + firstSegment + " position " + strconv.Itoa(rec1) + ": chainlog: record checksum mismatch: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Read error = %q, want the prefix %q", err, want)
	}
	if got, err := l.Read(t.Context(), 1, 1); err != nil || len(got) != 1 {
		t.Errorf("Read of the record before = %v, %v; want it", got, err)
	}
}

func TestObservability(t *testing.T) {
	logger, rec := newRecorder()
	dir := filepath.Join(t.TempDir(), "wal")
	opts := testOpts(512)
	opts.Logger = logger
	l := openLog(t, dir, opts, hooks{})
	attrs, ok := rec.find(slog.LevelInfo, "wal opened")
	wantSame(t, "wal opened", attrs, map[string]any{
		"dir": dir, "segments": int64(1), "oldest": uint64(1), "head": uint64(0), "checkpoint": uint64(0), "new": true,
	})
	if !ok {
		t.Fatal("no wal opened line")
	}
	for n := uint64(1); len(segmentNames(t, dir)) < 2; n++ {
		mustAppend(t, l, linkedBlocks(n, 1, 100)...)
	}
	names := segmentNames(t, dir)
	first, _ := parseSegmentName(names[1])
	attrs, _ = rec.find(slog.LevelDebug, "wal segment rolled")
	wantSame(t, "wal segment rolled", attrs, map[string]any{"segment": names[1], "offset": uint64(first)})
	head, _ := l.Head(t.Context())
	mustClose(t, l)

	openLog(t, dir, opts, hooks{})
	attrs, _ = rec.find(slog.LevelInfo, "wal opened")
	wantSame(t, "wal opened on reopen", attrs, map[string]any{
		"dir": dir, "segments": int64(2), "oldest": uint64(1), "head": uint64(head), "checkpoint": uint64(first - 1), "new": false,
	})
}

func TestOpenAfterInterruptedRoll(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	fail := false
	// Fail the first write to any segment but the first: the batch that rolls.
	h := hooks{write: func(f *os.File, p []byte) (int, error) {
		if fail && filepath.Base(f.Name()) != firstSegment {
			return 0, syscall.EIO
		}
		return f.Write(p)
	}}
	l := openLog(t, dir, testOpts(512), h)
	fail = true
	var before snapshotOf
	for n := uint64(1); ; n++ {
		before = snap(t, l)
		if _, err := l.Append(ctx, linkedBlocks(n, 1, 100)...); err != nil {
			if !errors.Is(err, ErrFailed) {
				t.Fatalf("Append: %v", err)
			}
			break
		}
	}
	mustClose(t, l)
	names := segmentNames(t, dir)
	if len(names) != 2 || listing(t, dir)[names[1]] != 0 {
		t.Fatalf("test setup: segments %v, want the first and an empty second", listing(t, dir))
	}
	noSegment := copyDir(t, dir)
	_ = os.Remove(filepath.Join(noSegment, names[1])) // the checkpoint was written, the segment never created

	for _, d := range []string{dir, noSegment} {
		l := openLog(t, d, testOpts(512), hooks{})
		wantSame(t, "log after the interrupted roll", snap(t, l), before)
		if off := mustAppend(t, l, linkedBlocks(uint64(before.head)+1, 1, 100)...); off != before.head+1 {
			t.Errorf("Append after reopen returned %d, want %d", off, before.head+1)
		}
		mustClose(t, l)
	}
}

func TestSegmentName(t *testing.T) {
	if got, want := segmentName(1), firstSegment; got != want {
		t.Errorf("segmentName(1) = %q, want %q", got, want)
	}
	if got, want := segmentName(18446744073709551615), "18446744073709551615.seg"; got != want {
		t.Errorf("segmentName(max) = %q, want %q", got, want)
	}
	tests := []struct {
		name  string
		first chainlog.Offset
		ok    bool
	}{
		{firstSegment, 1, true},
		{"00000000000000000300.seg", 300, true},
		{"18446744073709551615.seg", 18446744073709551615, true},
		{"18446744073709551616.seg", 0, false}, // above uint64
		{"0000000000000000001.seg", 0, false},  // 19 digits
		{"000000000000000000001.seg", 0, false},
		{"0000000000000000000a.seg", 0, false},
		{"00000000000000000001.txt", 0, false},
		{"meta", 0, false},
	}
	for _, tt := range tests {
		first, ok := parseSegmentName(tt.name)
		if first != tt.first || ok != tt.ok {
			t.Errorf("parseSegmentName(%q) = %d, %v; want %d, %v", tt.name, first, ok, tt.first, tt.ok)
		}
	}
}

// TestReadError covers the errors Read reports when reading a segment fails: a file closed by
// Close or deleted by Truncate under the reader gives ErrClosed or ErrTruncated.
func TestReadError(t *testing.T) {
	ioErr := errors.New("read failed")
	corrupt := fmt.Errorf("%w: segment x position 0: bad", ErrCorrupt)
	l := &Log{oldest: 5}
	if err := l.readError(3, corrupt); err != corrupt { // the same error is returned
		t.Errorf("readError of a corrupt record = %v, want it unchanged", err)
	}
	err := l.readError(3, ioErr)
	if !errors.Is(err, chainlog.ErrTruncated) || err.Error() != "chainlog: offset truncated: read from 3, oldest is 5" {
		t.Errorf("readError below Oldest = %v, want ErrTruncated", err)
	}
	if err := l.readError(5, ioErr); err != ioErr { // the same error is returned
		t.Errorf("readError of a retained offset = %v, want the I/O error", err)
	}
	l.closed = true
	if err := l.readError(5, ioErr); err != chainlog.ErrClosed { // unwrapped by contract
		t.Errorf("readError of a closed log = %v, want ErrClosed", err)
	}
}
