// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/consumer"
	"github.com/hmdsefi/evmsync/internal/fakechain"
	"github.com/hmdsefi/evmsync/tracker"
)

// Subject is the system under test.
type Subject interface {
	consumer.Consumer
	// Digest returns a digest of the subject's state. Equal states give equal digests; the digest
	// never depends on map order, the clock or the offset.
	Digest(ctx context.Context) ([]byte, error)
}

// Config parameterizes the harness for one kind of subject.
type Config struct {
	// Package is the `go test` package argument printed in the reproduce line, for example
	// "./internal/invariant" or "./memstore". Required.
	Package string
	// New returns a fresh subject: empty state, offset 0. Required.
	New func(t testing.TB) Subject
	// Reopen, if set, simulates a process restart: it returns a subject that resumes from old's
	// durable state (a memory subject returns a fresh one, which replays from offset 1). Nil
	// disables restarts.
	Reopen func(t testing.TB, old Subject) Subject
	// Expected, if set, returns the expected digest for the final canonical blocks
	// (CanonicalBlocks(Filter)), as an oracle independent of the subject.
	Expected func(blocks []chain.Block) []byte
	// Seeds and ShortSeeds are the seed counts without and with -short. 0 means 1,000 and 100.
	Seeds, ShortSeeds int
	// Steps is the number of chain steps per scenario. 0 means 200.
	Steps int
	// Parallel runs seeds as parallel subtests. Set it only when subjects of different seeds share
	// nothing.
	Parallel bool
	// Variant, if set, replaces VariantFor (tests of the harness itself).
	Variant func(seed uint64) Variant
	// Trace logs every step with t.Logf. Run sets it when one seed is selected.
	Trace bool
}

// withDefaults returns cfg with its zero fields defaulted. It panics if Package or New is missing.
func (cfg Config) withDefaults() Config {
	if cfg.Package == "" {
		panic("evmsync: invariant: Config.Package is empty")
	}
	if cfg.New == nil {
		panic("evmsync: invariant: Config.New is nil")
	}
	if cfg.Seeds == 0 {
		cfg.Seeds = 1000
	}
	if cfg.ShortSeeds == 0 {
		cfg.ShortSeeds = 100
	}
	if cfg.Steps == 0 {
		cfg.Steps = 200
	}
	if cfg.Variant == nil {
		cfg.Variant = VariantFor
	}
	return cfg
}

// Report summarizes one scenario.
type Report struct {
	Seed           uint64
	Entries        int    // entries in the log
	Blocks         int    // Block entries
	Reverts        int    // Revert entries
	MaxRevertDepth uint64 // largest (tip number - target number) over the Reverts
	Tolerated      int    // ErrNotFound answers the poll loop skipped
	Restarts       int    // Reopen calls
}

// The environment variables that select seeds.
const (
	envSeed  = "EVMSYNC_INVARIANT_SEED"
	envSeeds = "EVMSYNC_INVARIANT_SEEDS"
	envBase  = "EVMSYNC_INVARIANT_BASE"
)

// Run runs the selected seeds' scenarios as subtests named "seed=<n>" and fails each subtest on
// its first divergence, with a line that reproduces it.
//
// EVMSYNC_INVARIANT_SEED=<n> runs only seed n, with Trace on. Otherwise
// EVMSYNC_INVARIANT_SEEDS=<count> runs count seeds, and without it Run runs cfg.ShortSeeds seeds
// under -short and cfg.Seeds seeds otherwise. Seeds start at EVMSYNC_INVARIANT_BASE, or 1. Values
// are decimal or 0x hex. When at least 100 seeds ran, Run also fails unless at least half of them
// reverted and one reverted at least 3 blocks, so a generator that stops forking cannot pass.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	cfg = cfg.withDefaults()
	seeds, one, err := selectSeeds(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Trace = cfg.Trace || one
	root := strings.Split(t.Name(), "/")[0]

	var mu sync.Mutex
	var reports []Report // guarded by mu
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if reason := tameness(reports); reason != "" {
			t.Errorf("invariant: scenarios too tame: %s", reason)
		}
	})
	for _, seed := range seeds {
		t.Run("seed="+strconv.FormatUint(seed, 10), func(t *testing.T) {
			if cfg.Parallel {
				t.Parallel()
			}
			rep, err := Check(t, cfg, seed)
			mu.Lock()
			reports = append(reports, rep)
			mu.Unlock()
			if err != nil {
				t.Errorf("invariant: seed %d: %v\nreproduce: %s=%d go test %s -run '^%s$' -count=1 -v",
					seed, err, envSeed, seed, cfg.Package, root)
			}
		})
	}
}

// selectSeeds returns the seeds Run runs and whether one seed was selected.
func selectSeeds(cfg Config) ([]uint64, bool, error) {
	if s := os.Getenv(envSeed); s != "" {
		n, err := parseEnv(envSeed, s)
		return []uint64{n}, true, err
	}
	base := uint64(1)
	if s := os.Getenv(envBase); s != "" {
		var err error
		if base, err = parseEnv(envBase, s); err != nil {
			return nil, false, err
		}
	}
	count := uint64(cfg.Seeds) //nolint:gosec // a seed count is small
	if testing.Short() {
		count = uint64(cfg.ShortSeeds) //nolint:gosec // a seed count is small
	}
	if s := os.Getenv(envSeeds); s != "" {
		var err error
		if count, err = parseEnv(envSeeds, s); err != nil {
			return nil, false, err
		}
	}
	seeds := make([]uint64, 0, count)
	for i := range count {
		seeds = append(seeds, base+i)
	}
	return seeds, false, nil
}

func parseEnv(name, value string) (uint64, error) {
	n, err := strconv.ParseUint(value, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invariant: bad %s: %s", name, value)
	}
	return n, nil
}

// tameness returns why reports are too tame to test reorgs, or "" when they are not or when fewer
// than 100 seeds ran.
func tameness(reports []Report) string {
	if len(reports) < 100 {
		return ""
	}
	reverted, deep := 0, false
	for _, r := range reports {
		if r.Reverts >= 1 {
			reverted++
		}
		deep = deep || r.MaxRevertDepth >= 3
	}
	switch {
	case 2*reverted < len(reports):
		return fmt.Sprintf("%d of %d seeds reverted, want at least half", reverted, len(reports))
	case !deep:
		return fmt.Sprintf("no seed of %d reverted 3 or more blocks", len(reports))
	}
	return ""
}

// Check runs seed's scenario and returns its first divergence as an error. It fails t only for
// harness errors, such as New or Reopen returning nil. Check panics if cfg.Package is empty or
// cfg.New is nil.
//
// A scenario drives a fake chain with the seed's variant through a tracker into an in-memory log,
// and drains the subject from the log at random steps, with occasional restarts. It then settles
// the chain, drains the subject once more and requires: the log's structural replay leaves exactly
// the final canonical blocks; the subject's offset is the log head; the subject's digest equals
// the digest of a fresh subject drained over a log built from the canonical blocks alone; and
// cfg.Expected, if set, agrees.
func Check(t testing.TB, cfg Config, seed uint64) (Report, error) {
	t.Helper()
	out, err := check(t, cfg, seed)
	return out.report, err
}

// outcome is everything a scenario produced, for the harness's own tests.
type outcome struct {
	report Report
	log    []chainlog.Entry
	digest []byte // the subject's final digest; nil after an earlier divergence
}

// scenario is one seed's run.
type scenario struct {
	t    testing.TB
	cfg  Config
	seed uint64
	ctx  context.Context
	c    *fakechain.Chain // changed only through src.Do
	src  *fakechain.Source
	lg   *chainlog.Memory
	tr   *tracker.Tracker
	sub  Subject
	rep  Report
}

func check(t testing.TB, cfg Config, seed uint64) (outcome, error) {
	t.Helper()
	cfg = cfg.withDefaults()
	v := cfg.Variant(seed)
	c := fakechain.New(seed, v.Params)
	src := fakechain.NewSource(c, seed, v.Faults)
	s := &scenario{
		t:    t,
		cfg:  cfg,
		seed: seed,
		ctx:  context.Background(),
		c:    c,
		src:  src,
		lg:   chainlog.NewMemory(c.Meta(Filter), tracker.DefaultWindowCap),
		tr:   tracker.New(src, tracker.Config{Filter: Filter}, chainlog.NewState(tracker.DefaultWindowCap)),
		rep:  Report{Seed: seed},
	}
	g := c.Genesis().Ref
	es, err := s.tr.Finalize(s.ctx, g)
	if err := s.add("poll at step 0", "append at step 0", g, "finalize", es, err, false); err != nil {
		return s.outcome(), err
	}
	s.sub = s.newSubject()
	r := fakechain.NewRand(seed, "invariant/run")

	for i := range cfg.Steps {
		var ev fakechain.Event
		src.Do(func(c *fakechain.Chain) { ev = c.Step() })
		s.tracef("step %d: %s", i, ev)
		if err := s.poll(fmt.Sprintf("poll at step %d", i), fmt.Sprintf("append at step %d", i), true); err != nil {
			return s.outcome(), err
		}
		drain := r.Chance(500_000)
		restart := r.Chance(20_000)
		if drain {
			if err := s.drain(s.lg, s.sub); err != nil {
				return s.outcome(), fmt.Errorf("drain: %w", err)
			}
		}
		if restart && cfg.Reopen != nil {
			s.sub = cfg.Reopen(t, s.sub)
			if s.sub == nil {
				t.Fatalf("invariant: seed %d: Config.Reopen returned nil", seed)
			}
			s.rep.Restarts++
			s.tracef("restart")
		}
	}

	src.SetFaults(fakechain.Faults{})
	var head chain.BlockRef
	src.Do(func(c *fakechain.Chain) {
		c.Mine(1)
		c.SetFinalized(c.Head().Ref.Number)
		head = c.Head().Ref
	})
	if err := s.poll("settle", "settle", false); err != nil {
		return s.outcome(), err
	}
	st, err := s.lg.State(s.ctx)
	if err != nil {
		return s.outcome(), fmt.Errorf("settle: %w", err)
	}
	if st.Tip() != head || st.Finalized() != head {
		return s.outcome(), fmt.Errorf("settle: log tip %s, finalized %s, want head %s", st.Tip(), st.Finalized(), head)
	}
	if err := s.drain(s.lg, s.sub); err != nil {
		return s.outcome(), fmt.Errorf("drain: %w", err)
	}
	return s.verify(g)
}

// verify runs the final checks, in order, on a settled and drained scenario.
func (s *scenario) verify(g chain.BlockRef) (outcome, error) {
	var canon []chain.Block
	s.src.Do(func(c *fakechain.Chain) { canon = c.CanonicalBlocks(Filter) })
	out := s.outcome()

	live, _, err := replay(out.log, g)
	if err != nil {
		return out, err
	}
	for k := range max(len(live), len(canon)) {
		got, want := "none", "none"
		if k < len(live) {
			got = live[k].String()
		}
		if k < len(canon) {
			want = canon[k].Ref.String()
		}
		if got != want {
			return out, fmt.Errorf("structure: live block %d is %s, want %s", k, got, want)
		}
	}

	head, err := s.lg.Head(s.ctx)
	if err != nil {
		return out, fmt.Errorf("offset: %w", err)
	}
	off, err := s.sub.Offset(s.ctx)
	if err != nil {
		return out, fmt.Errorf("offset: %w", err)
	}
	if off != head {
		return out, fmt.Errorf("offset: %d, want %d", off, head)
	}

	digest, err := s.sub.Digest(s.ctx)
	if err != nil {
		return out, fmt.Errorf("digest: subject: %w", err)
	}
	out.digest = digest
	ref, err := s.reference(g, canon)
	if err != nil {
		return out, fmt.Errorf("digest: reference: %w", err)
	}
	if !bytes.Equal(digest, ref) {
		return out, fmt.Errorf("digest: subject %x, reference %x", digest, ref)
	}
	if s.cfg.Expected != nil {
		if want := s.cfg.Expected(canon); !bytes.Equal(digest, want) {
			return out, fmt.Errorf("expected: subject %x, oracle %x", digest, want)
		}
	}
	return out, nil
}

// poll asks the source for its head and finality, as an ingest loop would, and appends what the
// tracker returns. With tolerate, a not-found answer is counted and skipped; any other error is a
// divergence that starts with pollPrefix, or appendPrefix when Append refuses the entries. Without
// the finalized tag, finality is the head minus FallbackDepth, and the chain is told never to
// reorg below it.
func (s *scenario) poll(pollPrefix, appendPrefix string, tolerate bool) error {
	h, err := s.src.Latest(s.ctx)
	if err != nil {
		return fmt.Errorf("%s: latest: %w", pollPrefix, err)
	}
	es, err := s.tr.Head(s.ctx, h)
	if err := s.add(pollPrefix, appendPrefix, h.Ref, "head", es, err, tolerate); err != nil {
		return err
	}
	fh, err := s.src.Finalized(s.ctx)
	if errors.Is(err, chain.ErrUnsupported) {
		if h.Ref.Number < FallbackDepth {
			return nil
		}
		fh, err = s.src.HeaderByNumber(s.ctx, h.Ref.Number-FallbackDepth)
		if err == nil {
			s.src.Do(func(c *fakechain.Chain) {
				if fh.Ref.Number > c.Finalized().Ref.Number {
					c.SetFinalized(fh.Ref.Number)
				}
			})
		}
	}
	if err != nil {
		return s.add(pollPrefix, appendPrefix, h.Ref, "finalized", nil, err, tolerate)
	}
	es, err = s.tr.Finalize(s.ctx, fh.Ref)
	return s.add(pollPrefix, appendPrefix, h.Ref, "finalize", es, err, tolerate)
}

// add appends the result of one tracker or source call to the log. head is the polled head, for
// the trace.
func (s *scenario) add(pollPrefix, appendPrefix string, head chain.BlockRef, what string,
	es []chainlog.Entry, err error, tolerate bool,
) error {
	if err != nil {
		if tolerate && errors.Is(err, chain.ErrNotFound) {
			s.rep.Tolerated++
			s.tracef("tolerated: %v", err)
			return nil
		}
		return fmt.Errorf("%s: %s: %w", pollPrefix, what, err)
	}
	if len(es) == 0 {
		return nil
	}
	last, err := s.lg.Head(s.ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", appendPrefix, err)
	}
	if _, err := s.lg.Append(s.ctx, es...); err != nil {
		return fmt.Errorf("%s: offset %d: %s: %w", appendPrefix, last+1, describe(es), err)
	}
	s.tracef("head %d 0x%x: %s", head.Number, head.Hash[:4], describe(es))
	return nil
}

// describe returns "+block 41 +revert 38 +finalize 30" for entries.
func describe(es []chainlog.Entry) string {
	parts := make([]string, len(es))
	for i, e := range es {
		n := e.To.Number
		if e.Kind == chainlog.KindBlock {
			n = e.Block.Ref.Number
		}
		parts[i] = "+" + e.Kind.String() + " " + strconv.FormatUint(n, 10)
	}
	return strings.Join(parts, " ")
}

// drain applies lg's entries to sub up to the log head with consumer.Run, in the calling
// goroutine. Run must stop with context.Canceled right after the head's Apply, at offset head.
func (s *scenario) drain(lg chainlog.Log, sub Subject) error {
	head, err := lg.Head(s.ctx)
	if err != nil {
		return err
	}
	off, err := sub.Offset(s.ctx)
	if err != nil {
		return err
	}
	if off >= head {
		return nil
	}
	s.tracef("drain to %d", head)
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	if err := consumer.Run(ctx, lg, &cancelAt{Subject: sub, at: head, cancel: cancel}); err != context.Canceled { //nolint:errorlint // Run returns ctx.Err() unchanged
		return fmt.Errorf("run to offset %d: %w", head, err)
	}
	if off, err = sub.Offset(s.ctx); err != nil || off != head {
		return fmt.Errorf("offset %d after the run to %d (%v)", off, head, err)
	}
	return nil
}

// cancelAt wraps a subject and cancels the run right after its Apply of offset at succeeds.
type cancelAt struct {
	Subject
	at     chainlog.Offset
	cancel context.CancelFunc
}

func (c *cancelAt) Apply(ctx context.Context, e chainlog.Entry) error {
	err := c.Subject.Apply(ctx, e)
	if err == nil && e.Offset == c.at {
		c.cancel()
	}
	return err
}

// reference returns the digest of a fresh subject drained over a log of the final canonical
// blocks alone: the backfill path, sparse final blocks, against the realtime path of the scenario.
func (s *scenario) reference(g chain.BlockRef, canon []chain.Block) ([]byte, error) {
	meta := s.c.Meta(Filter)
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte("evmsync/invariant/reference"))
	h.Write(binary.BigEndian.AppendUint64(nil, s.seed))
	copy(meta.ID[:], h.Sum(nil))

	head := g
	if len(canon) > 0 {
		head = canon[len(canon)-1].Ref
	}
	lg := chainlog.NewMemory(meta, tracker.DefaultWindowCap)
	if _, err := lg.Append(s.ctx, referenceEntries(g, head, canon)...); err != nil {
		return nil, err
	}
	sub := s.newSubject()
	if err := s.drain(lg, sub); err != nil {
		return nil, err
	}
	return sub.Digest(s.ctx)
}

// referenceEntries returns Finalize g; then the blocks with at least one log, as final blocks, in
// chunks of up to 1,000, each chunk followed by a Finalize of its last block; then a Finalize of
// head when head is above the last block.
func referenceEntries(g, head chain.BlockRef, blocks []chain.Block) []chainlog.Entry {
	es := []chainlog.Entry{{Kind: chainlog.KindFinalize, To: g}}
	last, n := g, 0
	for _, b := range blocks {
		if len(b.Logs) == 0 {
			continue
		}
		es = append(es, chainlog.Entry{Kind: chainlog.KindBlock, Block: b, Final: true})
		last = b.Ref
		if n++; n == 1000 {
			es = append(es, chainlog.Entry{Kind: chainlog.KindFinalize, To: last})
			n = 0
		}
	}
	if n > 0 {
		es = append(es, chainlog.Entry{Kind: chainlog.KindFinalize, To: last})
	}
	if head.Number > last.Number {
		es = append(es, chainlog.Entry{Kind: chainlog.KindFinalize, To: head})
	}
	return es
}

// newSubject returns cfg.New's subject and fails the test if it is nil.
func (s *scenario) newSubject() Subject {
	sub := s.cfg.New(s.t)
	if sub == nil {
		s.t.Fatalf("invariant: seed %d: Config.New returned nil", s.seed)
	}
	return sub
}

// outcome returns the scenario's report, with the log statistics counted from the log, and the
// log itself.
func (s *scenario) outcome() outcome {
	out := outcome{report: s.rep}
	head, err := s.lg.Head(s.ctx)
	if err != nil || head == 0 {
		return out
	}
	out.log, err = s.lg.Read(s.ctx, 1, int(head)) //nolint:gosec // a scenario's log holds thousands of entries
	if err != nil {
		return out
	}
	_, st, _ := replay(out.log, s.c.Genesis().Ref)
	out.report.Entries = len(out.log)
	out.report.Blocks, out.report.Reverts, out.report.MaxRevertDepth = st.blocks, st.reverts, st.maxDepth
	return out
}

// replayStats counts a log's Block and Revert entries and its deepest Revert.
type replayStats struct {
	blocks, reverts int
	maxDepth        uint64
}

// replay replays entries onto a stack that starts at genesis g: a Block pushes its ref, a Revert
// pops every block above its target, which must then be the top (same number and hash), and a
// Finalize changes nothing. It returns the live blocks, the stack above g.
func replay(entries []chainlog.Entry, g chain.BlockRef) ([]chain.BlockRef, replayStats, error) {
	stack := []chain.BlockRef{g}
	var st replayStats
	for _, e := range entries {
		switch e.Kind {
		case chainlog.KindBlock:
			st.blocks++
			stack = append(stack, e.Block.Ref)
		case chainlog.KindRevert:
			st.reverts++
			top := stack[len(stack)-1]
			if e.To.Number > top.Number {
				return nil, st, fmt.Errorf("structure: offset %d: revert to %s above the top %s", e.Offset, e.To, top)
			}
			st.maxDepth = max(st.maxDepth, top.Number-e.To.Number)
			for len(stack) > 1 && stack[len(stack)-1].Number > e.To.Number {
				stack = stack[:len(stack)-1]
			}
			if top = stack[len(stack)-1]; top.Number != e.To.Number || top.Hash != e.To.Hash {
				return nil, st, fmt.Errorf("structure: offset %d: revert to %s, but the stack holds %s there",
					e.Offset, e.To, top)
			}
		}
	}
	return stack[1:], st, nil
}

// tracef logs one trace line when the configuration asks for it.
func (s *scenario) tracef(format string, args ...any) {
	if s.cfg.Trace {
		s.t.Logf(format, args...)
	}
}
