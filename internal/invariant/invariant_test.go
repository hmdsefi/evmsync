// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/internal/fakechain"
)

// memoryConfig is the configuration of TestReorgInvariant.
func memoryConfig() Config {
	return Config{
		Package:  "./internal/invariant",
		New:      func(testing.TB) Subject { return NewMemorySubject() },
		Reopen:   func(testing.TB, Subject) Subject { return NewMemorySubject() },
		Expected: ExpectedBalances,
		Parallel: true,
	}
}

func TestReorgInvariant(t *testing.T) {
	Run(t, memoryConfig())
}

func TestHarnessReplays(t *testing.T) {
	cfg := memoryConfig()
	var reverts, restarts, tolerated int
	for seed := uint64(1); seed <= 20; seed++ {
		first, err := check(t, cfg, seed)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		second, err := check(t, cfg, seed)
		if err != nil {
			t.Fatalf("seed %d, second run: %v", seed, err)
		}
		if first.report != second.report {
			t.Errorf("seed %d: reports %+v and %+v differ", seed, first.report, second.report)
		}
		if !reflect.DeepEqual(first.log, second.log) || len(first.log) != first.report.Entries {
			t.Errorf("seed %d: the two runs' logs differ, or the report counts %d of %d entries",
				seed, first.report.Entries, len(first.log))
		}
		if !bytes.Equal(first.digest, second.digest) || len(first.digest) != 32 {
			t.Errorf("seed %d: digests %x and %x", seed, first.digest, second.digest)
		}
		if first.report.Seed != seed {
			t.Errorf("seed %d: Report.Seed = %d", seed, first.report.Seed)
		}
		checkReport(t, seed, first.report, first.log)
		if first.report.Reverts > 0 {
			reverts++
		}
		restarts += first.report.Restarts
		tolerated += first.report.Tolerated
	}
	if reverts < 10 || restarts == 0 || tolerated == 0 {
		t.Errorf("seeds 1..20: %d with reverts, %d restarts, %d tolerated errors; the runs cover too little",
			reverts, restarts, tolerated)
	}
	rep, err := Check(t, cfg, 3)
	if err != nil || rep.Seed != 3 || rep.Entries == 0 {
		t.Errorf("Check(3) = %+v, %v", rep, err)
	}
}

// checkReport recounts the report's log statistics from the log.
func checkReport(t *testing.T, seed uint64, r Report, log []chainlog.Entry) {
	t.Helper()
	var blocks, reverts int
	var depth uint64
	stack := []chain.BlockRef{fakechain.New(seed, fakechain.Params{}).Genesis().Ref}
	for _, e := range log {
		switch e.Kind {
		case chainlog.KindBlock:
			blocks++
			stack = append(stack, e.Block.Ref)
		case chainlog.KindRevert:
			reverts++
			depth = max(depth, stack[len(stack)-1].Number-e.To.Number)
			for stack[len(stack)-1].Number > e.To.Number {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if r.Entries != len(log) || r.Blocks != blocks || r.Reverts != reverts || r.MaxRevertDepth != depth {
		t.Errorf("seed %d: report %+v, want Entries %d Blocks %d Reverts %d MaxRevertDepth %d",
			seed, r, len(log), blocks, reverts, depth)
	}
}

// ignoreReverts wraps the memory subject and drops every Revert entry: the orphaned blocks stay
// applied. It renumbers the entries it passes on so the inner subject sees dense offsets.
type ignoreReverts struct {
	Subject
	off     chainlog.Offset
	skipped chainlog.Offset
}

func (s *ignoreReverts) Offset(context.Context) (chainlog.Offset, error) { return s.off, nil }

func (s *ignoreReverts) Apply(ctx context.Context, e chainlog.Entry) error {
	if e.Kind == chainlog.KindRevert {
		s.skipped++
		s.off = e.Offset
		return nil
	}
	off := e.Offset
	e.Offset -= s.skipped
	if err := s.Subject.Apply(ctx, e); err != nil {
		return err
	}
	s.off = off
	return nil
}

func newIgnoreReverts(testing.TB) Subject { return &ignoreReverts{Subject: NewMemorySubject()} }

// dropFinalLogs wraps the memory subject and applies final blocks without their logs.
type dropFinalLogs struct{ Subject }

func (s dropFinalLogs) Apply(ctx context.Context, e chainlog.Entry) error {
	if e.Kind == chainlog.KindBlock && e.Final {
		e.Block.Logs = nil
	}
	return s.Subject.Apply(ctx, e)
}

// claimsOffset is a fresh memory subject that reports the offset of the subject it replaced, as
// if its state had survived a restart.
type claimsOffset struct {
	Subject
	off chainlog.Offset
}

func (s claimsOffset) Offset(context.Context) (chainlog.Offset, error) { return s.off, nil }

// constantDigest wraps the memory subject and reports the same digest whatever its state.
type constantDigest struct{ Subject }

func (constantDigest) Digest(context.Context) ([]byte, error) { return []byte{1}, nil }

func TestHarnessDetects(t *testing.T) {
	ctx := t.Context()
	base := memoryConfig()
	tests := []struct {
		name   string
		cfg    func(Config) Config
		prefix []string
	}{
		{"dropped logs", func(c Config) Config {
			c.Variant = func(seed uint64) Variant {
				v := VariantFor(seed)
				v.Faults.DroppedLogsPPM = 1_000_000
				return v
			}
			return c
		}, []string{"digest: ", "expected: "}},
		{"reverts ignored", func(c Config) Config {
			c.New = newIgnoreReverts
			c.Reopen = func(tb testing.TB, _ Subject) Subject { return newIgnoreReverts(tb) }
			return c
		}, []string{"digest: "}},
		{"final logs dropped", func(c Config) Config {
			c.New = func(testing.TB) Subject { return dropFinalLogs{NewMemorySubject()} }
			c.Reopen = func(testing.TB, Subject) Subject { return dropFinalLogs{NewMemorySubject()} }
			return c
		}, []string{"digest: "}},
		{"state lost on restart", func(c Config) Config {
			c.Reopen = func(tb testing.TB, old Subject) Subject {
				off, err := old.Offset(ctx)
				if err != nil {
					tb.Fatal(err)
				}
				return claimsOffset{NewMemorySubject(), off}
			}
			return c
		}, []string{"drain: "}},
		{"constant digest", func(c Config) Config {
			c.New = func(testing.TB) Subject { return constantDigest{NewMemorySubject()} }
			c.Reopen = func(testing.TB, Subject) Subject { return constantDigest{NewMemorySubject()} }
			return c
		}, []string{"expected: "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg(base)
			var errs []string
			for seed := uint64(1); seed <= 50; seed++ {
				if _, err := Check(t, cfg, seed); err != nil {
					errs = append(errs, err.Error())
					for _, p := range tt.prefix {
						if strings.HasPrefix(err.Error(), p) {
							return
						}
					}
				}
			}
			t.Errorf("no seed in 1..50 failed with %q; errors: %q", tt.prefix, errs)
		})
	}
}

func TestDivergenceTexts(t *testing.T) {
	cfg := memoryConfig()
	cfg.New = func(testing.TB) Subject { return constantDigest{NewMemorySubject()} }
	cfg.Reopen = func(testing.TB, Subject) Subject { return constantDigest{NewMemorySubject()} }
	cfg.Expected = nil
	if _, err := Check(t, cfg, 1); err != nil {
		t.Errorf("constant digest without Expected: %v, want no divergence", err)
	}

	cfg = memoryConfig()
	cfg.New = newIgnoreReverts
	cfg.Reopen = nil
	for seed := uint64(1); seed <= 50; seed++ {
		_, err := Check(t, cfg, seed)
		if err == nil {
			continue
		}
		if !strings.HasPrefix(err.Error(), "digest: subject ") || !strings.Contains(err.Error(), ", reference ") {
			t.Fatalf("seed %d: %q, want \"digest: subject <hex>, reference <hex>\"", seed, err)
		}
		return
	}
	t.Error("no seed in 1..50 diverged")
}

func TestStructuralReplay(t *testing.T) {
	c := fakechain.New(1, fakechain.Params{})
	g := c.Genesis().Ref
	a := c.Mine(4)
	b := c.Reorg(2, 3)
	blk := func(r chain.BlockRef) chainlog.Entry {
		return chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r}}
	}
	es := []chainlog.Entry{
		{Kind: chainlog.KindFinalize, To: g},
		blk(a[0]), blk(a[1]), blk(a[2]), blk(a[3]),
		{Kind: chainlog.KindRevert, To: a[1]},
		blk(b[0]), blk(b[1]), blk(b[2]),
		{Kind: chainlog.KindFinalize, To: b[0]},
		{Kind: chainlog.KindRevert, To: b[1]},
	}
	live, st, err := replay(es, g)
	want := []chain.BlockRef{a[0], a[1], b[0], b[1]}
	if err != nil || !reflect.DeepEqual(live, want) {
		t.Fatalf("replay = %v, %v; want %v", live, err, want)
	}
	if st.blocks != 7 || st.reverts != 2 || st.maxDepth != 2 {
		t.Errorf("replay stats %+v, want 7 blocks, 2 reverts, depth 2", st)
	}

	// A Revert whose target is not on the stack is an error.
	bad := slices.Concat(es[:5], []chainlog.Entry{{Kind: chainlog.KindRevert, To: b[0]}})
	if _, _, err := replay(bad, g); err == nil || !strings.HasPrefix(err.Error(), "structure: ") {
		t.Errorf("replay with a foreign revert target: %v, want a structure error", err)
	}
	if live, _, err := replay(nil, g); err != nil || len(live) != 0 {
		t.Errorf("replay(nil) = %v, %v", live, err)
	}
}

func TestReferenceLog(t *testing.T) {
	c := fakechain.New(5, fakechain.Params{})
	c.Mine(6000)
	blocks := c.CanonicalBlocks(Filter)
	es := referenceEntries(c.Genesis().Ref, c.Head().Ref, blocks)
	if !reflect.DeepEqual(es[0], chainlog.Entry{Kind: chainlog.KindFinalize, To: c.Genesis().Ref}) {
		t.Fatalf("first entry %+v, want Finalize genesis", es[0])
	}
	var want []chain.Block
	for _, b := range blocks {
		if len(b.Logs) > 0 {
			want = append(want, b)
		}
	}
	rest := es[1:]
	if want[len(want)-1].Ref != c.Head().Ref {
		if last := rest[len(rest)-1]; !reflect.DeepEqual(last, chainlog.Entry{Kind: chainlog.KindFinalize, To: c.Head().Ref}) {
			t.Fatalf("last entry %+v, want Finalize of the head", last)
		}
		rest = rest[:len(rest)-1]
	}
	var got []chain.Block
	var chunks []int
	n := 0
	for i, e := range rest {
		switch e.Kind {
		case chainlog.KindBlock:
			if !e.Final {
				t.Fatalf("entry %d: block %d is not final", i+1, e.Block.Ref.Number)
			}
			got = append(got, e.Block)
			n++
		case chainlog.KindFinalize:
			if n == 0 || e.To != got[len(got)-1].Ref {
				t.Fatalf("entry %d: Finalize %v, want the chunk's last block", i+1, e.To)
			}
			chunks = append(chunks, n)
			n = 0
		default:
			t.Fatalf("entry %d: kind %s", i+1, e.Kind)
		}
	}
	if !reflect.DeepEqual(got, want) || len(want) <= 2000 {
		t.Errorf("reference blocks: %d, want the %d canonical blocks with a matching log", len(got), len(want))
	}
	for i, k := range chunks {
		if n != 0 || k > 1000 || (i < len(chunks)-1 && k != 1000) {
			t.Fatalf("chunks %v (open %d), want 1000 each but the last", chunks, n)
		}
	}
	lg := chainlog.NewMemory(chainlog.Meta{ChainID: 31337, Filter: Filter}, 0)
	if _, err := lg.Append(t.Context(), es...); err != nil {
		t.Errorf("the reference entries do not form a valid log: %v", err)
	}

	// A head without matching logs above the last block gets its own Finalize.
	g := c.Genesis().Ref
	head := c.Head().Ref
	short := referenceEntries(g, head, want[:1])
	wantShort := []chainlog.Entry{
		{Kind: chainlog.KindFinalize, To: g},
		{Kind: chainlog.KindBlock, Block: want[0], Final: true},
		{Kind: chainlog.KindFinalize, To: want[0].Ref},
		{Kind: chainlog.KindFinalize, To: head},
	}
	if !reflect.DeepEqual(short, wantShort) {
		t.Errorf("referenceEntries(one block) = %+v, want %+v", short, wantShort)
	}
	if es := referenceEntries(g, g, nil); !reflect.DeepEqual(es, []chainlog.Entry{{Kind: chainlog.KindFinalize, To: g}}) {
		t.Errorf("referenceEntries(no blocks, head G) = %+v", es)
	}
}

// logTB records Logf lines.
type logTB struct {
	testing.TB
	lines []string
}

func (l *logTB) Logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestTrace(t *testing.T) {
	cfg := memoryConfig()
	quiet := &logTB{TB: t}
	if _, err := Check(quiet, cfg, 2); err != nil {
		t.Fatal(err)
	}
	if len(quiet.lines) != 0 {
		t.Errorf("Trace off logged %d lines: %q", len(quiet.lines), quiet.lines[0])
	}
	cfg.Trace = true
	loud := &logTB{TB: t}
	if _, err := Check(loud, cfg, 2); err != nil {
		t.Fatal(err)
	}
	prefixes := []string{"step 0: ", "step 199: ", "head ", "drain to ", "tolerated: "}
	for _, p := range prefixes {
		found := false
		for _, l := range loud.lines {
			found = found || strings.HasPrefix(l, p)
		}
		if !found {
			t.Errorf("no trace line starts with %q", p)
		}
	}
	for _, l := range loud.lines {
		if strings.HasPrefix(l, "head ") && !strings.Contains(l, ": +") {
			t.Errorf("append line %q has no +<kind> part", l)
		}
	}
}

func TestConfigMisuse(t *testing.T) {
	cfg := memoryConfig()
	cfg.Package = ""
	wantPanic(t, "evmsync: invariant: Config.Package is empty", func() { _, _ = Check(t, cfg, 1) })
	cfg = memoryConfig()
	cfg.New = nil
	wantPanic(t, "evmsync: invariant: Config.New is nil", func() { _, _ = Check(t, cfg, 1) })
}

func wantPanic(t *testing.T, msg string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if r := recover(); r != msg {
			t.Errorf("panic %v, want %q", r, msg)
		}
	}()
	fn()
}
