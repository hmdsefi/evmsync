// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/internal/fakechain"
	"github.com/hmdsefi/evmsync/tracker"
)

var flt = fakechain.DefaultFilter

// env is one tracker over a fake chain, with the log its results are appended to.
type env struct {
	c     *fakechain.Chain
	src   *fakechain.Source      // records the calls; the tracker may see it through a wrapper
	tr    *tracker.Tracker       // reads src, or the wrapper around it
	lg    *chainlog.Memory       // every returned batch is appended here and must succeed
	names map[common.Hash]string // "G", "A5", "B7"… for call traces and entry lists
	cfg   tracker.Config         // the tracker's configuration; a zero Filter becomes flt
	s     chain.Source           // the Source the tracker reads
	g     chain.BlockRef         // genesis
}

// newBareEnv returns an env over fakechain.New(1, Params{}) with an unanchored tracker. A nil wrap
// gives the tracker src itself.
func newBareEnv(t *testing.T, cfg tracker.Config, f fakechain.Faults, wrap func(chain.Source) chain.Source) *env {
	t.Helper()
	c := fakechain.New(1, fakechain.Params{})
	src := fakechain.NewSource(c, 1, f)
	var s chain.Source = src
	if wrap != nil {
		s = wrap(src)
	}
	if cfg.Filter.Addresses == nil && cfg.Filter.Topics == nil {
		cfg.Filter = flt
	}
	g := c.Genesis().Ref
	return &env{
		c:     c,
		src:   src,
		tr:    tracker.New(s, cfg, chainlog.NewState(tracker.DefaultWindowCap)),
		lg:    chainlog.NewMemory(c.Meta(flt), tracker.DefaultWindowCap),
		names: map[common.Hash]string{g.Hash: "G"},
		cfg:   cfg,
		s:     s,
		g:     g,
	}
}

// newEnv returns an env whose tracker is anchored at genesis.
func newEnv(t *testing.T, cfg tracker.Config, f fakechain.Faults, wrap func(chain.Source) chain.Source) *env {
	t.Helper()
	e := newBareEnv(t, cfg, f, wrap)
	got, err := e.finalize(t, e.g)
	if err != nil {
		t.Fatalf("Finalize(G): %v", err)
	}
	e.want(t, got, e.fin(e.g))
	return e
}

// tracked returns an env anchored at genesis, with the chain mined to block n of branch A and
// Head of that block done.
func tracked(t *testing.T, n int) *env {
	t.Helper()
	e := newEnv(t, tracker.Config{}, fakechain.Faults{}, nil)
	refs := e.mine("A", n)
	e.mustHead(t, refs[n-1])
	return e
}

// label names refs "<branch><Number>".
func (e *env) label(branch string, refs []chain.BlockRef) {
	for _, r := range refs {
		e.names[r.Hash] = fmt.Sprintf("%s%d", branch, r.Number)
	}
}

// mine mines n blocks through the Source's lock and names them on branch.
func (e *env) mine(branch string, n int) []chain.BlockRef {
	var refs []chain.BlockRef
	e.src.Do(func(c *fakechain.Chain) { refs = c.Mine(n) })
	e.label(branch, refs)
	return refs
}

// reorg runs Chain.Reorg through the Source's lock and names the new blocks on branch.
func (e *env) reorg(branch string, depth uint64, n int) []chain.BlockRef {
	var refs []chain.BlockRef
	e.src.Do(func(c *fakechain.Chain) { refs = c.Reorg(depth, n) })
	e.label(branch, refs)
	return refs
}

// head resets the call record, calls Head with h and appends a non-empty result to the log.
func (e *env) head(t *testing.T, h chain.BlockRef) ([]chainlog.Entry, error) {
	t.Helper()
	return e.headHeader(t, chain.Header{Ref: h})
}

func (e *env) headHeader(t *testing.T, h chain.Header) ([]chainlog.Entry, error) {
	t.Helper()
	e.src.ResetCalls()
	got, err := e.tr.Head(t.Context(), h)
	e.appendAll(t, got, err)
	return got, err
}

// finalize resets the call record, calls Finalize with f and appends a non-empty result to the
// log.
func (e *env) finalize(t *testing.T, f chain.BlockRef) ([]chainlog.Entry, error) {
	t.Helper()
	e.src.ResetCalls()
	got, err := e.tr.Finalize(t.Context(), f)
	e.appendAll(t, got, err)
	return got, err
}

func (e *env) appendAll(t *testing.T, got []chainlog.Entry, err error) {
	t.Helper()
	if err != nil {
		if got != nil {
			t.Fatalf("entries %s returned with error %v", e.show(got), err)
		}
		return
	}
	if len(got) == 0 {
		return
	}
	if _, aerr := e.lg.Append(t.Context(), got...); aerr != nil {
		t.Fatalf("Append(%s): %v", e.show(got), aerr)
	}
	e.sameState(t)
}

// mustHead calls head and fails the test on an error.
func (e *env) mustHead(t *testing.T, h chain.BlockRef) []chainlog.Entry {
	t.Helper()
	got, err := e.head(t, h)
	if err != nil {
		t.Fatalf("Head(%s): %v", e.name(h.Hash), err)
	}
	return got
}

// mustFinalize calls finalize and fails the test on an error.
func (e *env) mustFinalize(t *testing.T, f chain.BlockRef) []chainlog.Entry {
	t.Helper()
	got, err := e.finalize(t, f)
	if err != nil {
		t.Fatalf("Finalize(%s): %v", e.name(f.Hash), err)
	}
	return got
}

// calls returns the recorded calls, for example []string{"H(A9)", "H(A8)", "L(A8)"}.
func (e *env) calls() []string {
	var out []string
	for _, c := range e.src.Calls() {
		switch c.Method {
		case "HeaderByHash":
			out = append(out, "H("+e.name(c.Hash)+")")
		case "Logs":
			out = append(out, "L("+e.name(c.Hash)+")")
		default:
			out = append(out, c.Method)
		}
	}
	return out
}

// wantCalls fails the test unless the recorded calls are want, written "H(A5) L(A5)".
func (e *env) wantCalls(t *testing.T, want string) {
	t.Helper()
	if got := strings.Join(e.calls(), " "); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

// name returns the label of h, or its first four bytes.
func (e *env) name(h common.Hash) string {
	if n, ok := e.names[h]; ok {
		return n
	}
	return h.Hex()[:10]
}

// blk returns the Block entry of x with its logs that match flt.
func (e *env) blk(x chain.BlockRef) chainlog.Entry {
	b, ok := e.c.Block(x.Hash, flt)
	if !ok {
		panic("blk of an unknown block " + x.String())
	}
	return chainlog.Entry{Kind: chainlog.KindBlock, Block: b}
}

func (e *env) rev(x chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindRevert, To: x}
}

func (e *env) fin(x chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindFinalize, To: x}
}

// show formats entries as "rev(A4) blk(B5)".
func (e *env) show(entries []chainlog.Entry) string {
	parts := make([]string, len(entries))
	for i, en := range entries {
		switch en.Kind {
		case chainlog.KindBlock:
			parts[i] = "blk(" + e.name(en.Block.Ref.Hash) + ")"
		case chainlog.KindRevert:
			parts[i] = "rev(" + e.name(en.To.Hash) + ")"
		case chainlog.KindFinalize:
			parts[i] = "fin(" + e.name(en.To.Hash) + ")"
		default:
			parts[i] = en.Kind.String()
		}
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// want fails the test unless got deep-equals want.
func (e *env) want(t *testing.T, got []chainlog.Entry, want ...chainlog.Entry) {
	t.Helper()
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %s, want %s\n got  %+v\n want %+v", e.show(got), e.show(want), got, want)
	}
}

// view is the part of a chainlog.State the tracker must keep equal to the log's.
type view struct {
	Tip, Finalized chain.BlockRef
	Window         []chain.BlockRef
}

func viewOf(s chainlog.State) view {
	return view{Tip: s.Tip(), Finalized: s.Finalized(), Window: s.Window()}
}

// sameState fails the test unless the tracker's State equals the log's.
func (e *env) sameState(t *testing.T) {
	t.Helper()
	ls, err := e.lg.State(t.Context())
	if err != nil {
		t.Fatalf("log State: %v", err)
	}
	if got, want := viewOf(e.tr.State()), viewOf(ls); !reflect.DeepEqual(got, want) {
		t.Errorf("tracker state = %+v, log state %+v", got, want)
	}
}

// unchanged fails the test unless the tracker's view equals before.
func (e *env) unchanged(t *testing.T, before view) {
	t.Helper()
	if after := viewOf(e.tr.State()); !reflect.DeepEqual(after, before) {
		t.Errorf("state changed: %+v, want %+v", after, before)
	}
}

// refs returns the canonical refs of numbers from..to.
func (e *env) refs(from, to uint64) []chain.BlockRef {
	var out []chain.BlockRef
	for n := from; n <= to; n++ {
		h, ok := e.c.Canonical(n)
		if !ok {
			panic(fmt.Sprintf("no canonical block %d", n))
		}
		out = append(out, h.Ref)
	}
	return out
}

// blks returns the Block entries of refs.
func (e *env) blks(refs ...chain.BlockRef) []chainlog.Entry {
	out := make([]chainlog.Entry, len(refs))
	for i, r := range refs {
		out[i] = e.blk(r)
	}
	return out
}

// wantErr fails the test unless err wraps target and has the text want.
func wantErr(t *testing.T, err, target error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %q", want)
	}
	if !errors.Is(err, target) {
		t.Errorf("err %q does not wrap %q", err, target)
	}
	if err.Error() != want {
		t.Errorf("err = %q\nwant  %q", err.Error(), want)
	}
}

// wrapSource overrides some methods of the Source it wraps.
type wrapSource struct {
	chain.Source
	header func(ctx context.Context, h common.Hash) (chain.Header, error)
	logs   func(ctx context.Context, h common.Hash, f chain.Filter) ([]chain.Log, error)
}

func (w *wrapSource) HeaderByHash(ctx context.Context, h common.Hash) (chain.Header, error) {
	if w.header != nil {
		return w.header(ctx, h)
	}
	return w.Source.HeaderByHash(ctx, h)
}

func (w *wrapSource) Logs(ctx context.Context, h common.Hash, f chain.Filter) ([]chain.Log, error) {
	if w.logs != nil {
		return w.logs(ctx, h, f)
	}
	return w.Source.Logs(ctx, h, f)
}

// mustPanic fails the test unless fn panics with the value want.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if got := fmt.Sprint(recover()); got != want {
			t.Errorf("panic = %q, want %q", got, want)
		}
	}()
	fn()
}
