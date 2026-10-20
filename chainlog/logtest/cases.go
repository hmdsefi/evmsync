// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package logtest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// testCase is one subtest of Run. fresh opens another empty log for the cases that need two.
type testCase struct {
	name string
	run  func(t *testing.T, l chainlog.Log, fresh func() chainlog.Log)
}

func cases() []testCase {
	return []testCase{
		{"Empty", testEmpty},
		{"Meta", testMeta},
		{"AppendOffsets", testAppendOffsets},
		{"AppendIgnoresOffset", testAppendIgnoresOffset},
		{"AppendEmpty", testAppendEmpty},
		{"RoundTrip", testRoundTrip},
		{"ReadBounds", testReadBounds},
		{"Ownership", testOwnership},
		{"Rule1Increasing", testRule1Increasing},
		{"Rule2Linked", testRule2Linked},
		{"Rule3RevertTarget", testRule3RevertTarget},
		{"Rule4Finality", testRule4Finality},
		{"Rule5FinalBlocks", testRule5FinalBlocks},
		{"Rule6Window", testRule6Window},
		{"Shape", testShape},
		{"TooLarge", testTooLarge},
		{"BatchAtomic", testBatchAtomic},
		{"StateCopy", testStateCopy},
		{"Wait", testWait},
		{"Truncate", testTruncate},
		{"TruncateBound", testTruncateBound},
		{"Closed", testClosed},
		{"CancelledContext", testCancelledContext},
		{"Concurrent", testConcurrent},
		{"BatchVisibility", testBatchVisibility},
	}
}

func block(r chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r}}
}

func finalBlock(r chain.BlockRef) chainlog.Entry {
	e := block(r)
	e.Final = true
	return e
}

func revert(r chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindRevert, To: r}
}

func finalize(r chain.BlockRef) chainlog.Entry {
	return chainlog.Entry{Kind: chainlog.KindFinalize, To: r}
}

// blocks returns the linked blocks from..to of chain 0.
func blocks(from, to uint64) []chainlog.Entry {
	var es []chainlog.Entry
	for n := from; n <= to; n++ {
		es = append(es, block(blk(n, 0)))
	}
	return es
}

// numbered returns copies of es with offsets 1, 2, ...
func numbered(es ...chainlog.Entry) []chainlog.Entry {
	out := make([]chainlog.Entry, len(es))
	for i, e := range es {
		e.Offset = chainlog.Offset(i + 1) //nolint:gosec // i < len(es)
		out[i] = e
	}
	return out
}

// noErr stops the test when err is not nil.
func noErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// eq reports got when it does not deep-equal want.
func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s =\n%+v\nwant\n%+v", what, got, want)
	}
}

// wantErr checks that err wraps target and, unless text is empty, reads text.
func wantErr(t *testing.T, what string, err, target error, text string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s error = %v, want one wrapping %v", what, err, target)
	}
	if text != "" {
		eq(t, what+" error", err.Error(), text)
	}
}

func appendOK(t *testing.T, l chainlog.Log, es ...chainlog.Entry) chainlog.Offset {
	t.Helper()
	off, err := l.Append(t.Context(), es...)
	noErr(t, err, "Append")
	return off
}

// wantRejected checks that appending the single entry e fails with the malformed detail and
// leaves Head unchanged.
func wantRejected(t *testing.T, l chainlog.Log, e chainlog.Entry, detail string) {
	t.Helper()
	before := head(t, l)
	_, err := l.Append(t.Context(), e)
	wantErr(t, "Append", err, chainlog.ErrMalformed, "chainlog: append entry 1 of 1: chainlog: malformed entry: "+detail)
	eq(t, "Head after a rejected Append", head(t, l), before)
}

func head(t *testing.T, l chainlog.Log) chainlog.Offset {
	t.Helper()
	h, err := l.Head(t.Context())
	noErr(t, err, "Head")
	return h
}

func oldest(t *testing.T, l chainlog.Log) chainlog.Offset {
	t.Helper()
	o, err := l.Oldest(t.Context())
	noErr(t, err, "Oldest")
	return o
}

func state(t *testing.T, l chainlog.Log) chainlog.State {
	t.Helper()
	s, err := l.State(t.Context())
	noErr(t, err, "State")
	return s
}

func read(t *testing.T, l chainlog.Log, from chainlog.Offset, max int) []chainlog.Entry {
	t.Helper()
	es, err := l.Read(t.Context(), from, max)
	noErr(t, err, fmt.Sprintf("Read(%d, %d)", from, max))
	return es
}

// wantEntries checks that got deep-equals want.
func wantEntries(t *testing.T, got, want []chainlog.Entry) {
	t.Helper()
	eq(t, "entries", got, want)
}

// view is what a state shows through its accessors.
type view struct {
	tip, fin chain.BlockRef
	win      []chain.BlockRef
}

func viewOf(s chainlog.State) view { return view{s.Tip(), s.Finalized(), s.Window()} }

func wantView(t *testing.T, l chainlog.Log, want view) {
	t.Helper()
	eq(t, "state", viewOf(state(t, l)), want)
}

func refs(rs ...chain.BlockRef) []chain.BlockRef { return rs }

func testEmpty(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	eq(t, "Head", head(t, l), chainlog.Offset(0))
	eq(t, "Oldest", oldest(t, l), chainlog.Offset(1))
	wantView(t, l, view{})
	for _, from := range []chainlog.Offset{0, 1} {
		eq(t, fmt.Sprintf("Read(%d, 10)", from), read(t, l, from, 10), []chainlog.Entry(nil))
	}
}

func testMeta(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	m, err := l.Meta(t.Context())
	noErr(t, err, "Meta")
	eq(t, "Meta", m, Meta())
	m.ID[0] = 0xff
	m.Filter.Addresses[0][0] = 0xff
	m.Filter.Topics[0][0][0] = 0xff
	m.Filter.Topics[0] = append(m.Filter.Topics[0], [32]byte{1})
	again, err := l.Meta(t.Context())
	noErr(t, err, "Meta")
	eq(t, "Meta after mutating an earlier result", again, Meta())
}

func testAppendOffsets(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	eq(t, "Append of 3 entries", appendOK(t, l, blocks(101, 103)...), chainlog.Offset(3))
	wantEntries(t, read(t, l, 1, 10), numbered(blocks(101, 103)...))
	eq(t, "Append of 2 more entries", appendOK(t, l, blocks(104, 105)...), chainlog.Offset(5))
}

func testAppendIgnoresOffset(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	es := blocks(101, 102)
	es[0].Offset, es[1].Offset = 99, 99
	eq(t, "Append", appendOK(t, l, es...), chainlog.Offset(2))
	wantEntries(t, read(t, l, 1, 10), numbered(blocks(101, 102)...))
}

func testAppendEmpty(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	eq(t, "Append() on an empty log", appendOK(t, l), chainlog.Offset(0))
	appendOK(t, l, blocks(101, 102)...)
	before := viewOf(state(t, l))
	eq(t, "Append()", appendOK(t, l), chainlog.Offset(2))
	eq(t, "Head", head(t, l), chainlog.Offset(2))
	wantView(t, l, before)
}

// logOf returns a log of block r with ntopics topics and dataLen bytes of data. Empty Topics and
// Data are non-nil when empty is set.
func logOf(r chain.BlockRef, index uint32, ntopics, dataLen int, empty bool) chain.Log {
	l := chain.Log{BlockNumber: r.Number, BlockHash: r.Hash, TxIndex: index / 2, Index: index}
	l.Address[19] = 0xaa
	binary.BigEndian.PutUint32(l.Address[:4], index)
	l.TxHash = h(uint64(index), 9)
	for i := range ntopics {
		var topic [32]byte
		topic[0] = byte(i + 1)
		binary.BigEndian.PutUint32(topic[28:], index)
		l.Topics = append(l.Topics, topic)
	}
	if dataLen > 0 {
		l.Data = make([]byte, dataLen)
		for i := range l.Data {
			l.Data[i] = byte(i)
		}
	}
	if empty {
		l.Topics = append(l.Topics, [32]byte{})[:0]
		l.Data = []byte{}
	}
	return l
}

func testRoundTrip(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	b101, b102, b103 := blk(101, 0), blk(102, 0), blk(103, 0)
	in := []chainlog.Entry{
		block(b101),
		{Kind: chainlog.KindBlock, Block: chain.Block{Ref: b102, Logs: []chain.Log{logOf(b102, 0, 4, 300, false)}}},
		{Kind: chainlog.KindBlock, Block: chain.Block{Ref: b103, Logs: []chain.Log{
			logOf(b103, 0, 0, 0, true), logOf(b103, 1, 1, 1, false), logOf(b103, 3, 2, 7, false), logOf(b103, 5, 3, 0, false),
		}}},
		revert(b102),
		finalize(b101),
	}
	appendOK(t, l, in...)

	want := numbered(in...)
	want[2].Block.Logs = []chain.Log{logOf(b103, 0, 0, 0, false), logOf(b103, 1, 1, 1, false), logOf(b103, 3, 2, 7, false), logOf(b103, 5, 3, 0, false)}
	got := read(t, l, 1, 10)
	wantEntries(t, got, want)
	l0 := got[2].Block.Logs[0]
	eq(t, "empty Topics and Data", [2]bool{l0.Topics == nil, l0.Data == nil}, [2]bool{true, true})
}

func testReadBounds(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	all := numbered(blocks(101, 105)...)
	appendOK(t, l, blocks(101, 105)...)
	wantEntries(t, read(t, l, 2, 2), all[1:3])
	wantEntries(t, read(t, l, 5, 100), all[4:5])
	wantEntries(t, read(t, l, 0, 1), all[0:1])
	wantEntries(t, read(t, l, 1, math.MaxInt), all)
	eq(t, "Read(6, 1)", read(t, l, 6, 1), []chainlog.Entry(nil))
	_, err := l.Read(t.Context(), 7, 1)
	wantErr(t, "Read(7, 1)", err, chainlog.ErrAhead, "chainlog: offset beyond head: read from 7, head is 5")
	for _, max := range []int{0, -1} {
		eq(t, fmt.Sprintf("Read(1, %d) panic", max), panicText(func() { _, _ = l.Read(t.Context(), 1, max) }),
			fmt.Sprintf("evmsync: chainlog: Read max must be at least 1, got %d", max))
	}
}

func panicText(f func()) (s string) {
	defer func() { s = fmt.Sprint(recover()) }()
	f()
	return ""
}

func testOwnership(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	r := blk(101, 0)
	e := chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r, Logs: []chain.Log{logOf(r, 0, 1, 3, false)}}}
	want := numbered(chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r, Logs: []chain.Log{logOf(r, 0, 1, 3, false)}}})
	appendOK(t, l, e)
	e.Block.Logs[0].Data[0] = 0xee
	e.Block.Logs[0].Topics[0][0] = 0xee
	got := read(t, l, 1, 1)
	wantEntries(t, got, want)
	got[0].Block.Logs[0].Data[0] = 0xdd
	got[0].Block.Logs[0].Topics[0][0] = 0xdd
	wantEntries(t, read(t, l, 1, 1), want)
}

func testRule1Increasing(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	appendOK(t, l, block(blk(101, 0)))
	wantRejected(t, l, block(blk(101, 0)), "block 101 is not above tip "+blk(101, 0).String())
	appendOK(t, l, block(blk(102, 0)))
	wantRejected(t, l, block(blk(101, 0)), "block 101 is not above tip "+blk(102, 0).String())
	wantRejected(t, l, block(blk(102, 1)), "block 102 is not above tip "+blk(102, 0).String())
}

func testRule2Linked(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	appendOK(t, l, block(blk(101, 0)))
	bad := blk(102, 0)
	bad.Parent = h(101, 1)
	wantRejected(t, l, block(bad), "block 102: parent "+bad.Parent.Hex()+" is not tip hash "+blk(101, 0).Hash.Hex())
	wantRejected(t, l, block(blk(105, 0)), "block 105: non-final block is not the child of tip "+blk(101, 0).String())
	appendOK(t, l, finalize(blk(101, 0)))
	unlinked := blk(105, 0)
	unlinked.Parent = h(99, 5)
	eq(t, "Append of a Final block 105", appendOK(t, l, finalBlock(unlinked)), chainlog.Offset(3))
	wantView(t, l, view{tip: unlinked, fin: unlinked})
}

func testRule3RevertTarget(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	wantRejected(t, l, revert(blk(101, 0)), "revert to "+blk(101, 0).String()+": target is not below tip "+chain.BlockRef{}.String())
	appendOK(t, l, finalize(blk(100, 0)))
	appendOK(t, l, blocks(101, 103)...)
	tip := blk(103, 0).String()
	wantRejected(t, l, revert(blk(103, 0)), "revert to "+blk(103, 0).String()+": target is not below tip "+tip)
	wantRejected(t, l, revert(blk(104, 0)), "revert to "+blk(104, 0).String()+": target is not below tip "+tip)
	wantRejected(t, l, revert(blk(102, 1)), "revert to "+blk(102, 1).String()+": target is neither the finalized block nor in the window")
	appendOK(t, l, revert(blk(102, 0)))
	wantView(t, l, view{tip: blk(102, 0), fin: blk(100, 0), win: refs(blk(101, 0), blk(102, 0))})
	appendOK(t, l, revert(blk(100, 0)))
	wantView(t, l, view{tip: blk(100, 0), fin: blk(100, 0)})
}

func testRule4Finality(t *testing.T, l chainlog.Log, fresh func() chainlog.Log) {
	appendOK(t, l, blocks(101, 104)...)
	wantRejected(t, l, finalize(blk(100, 1)), "finalize to "+blk(100, 1).String()+": window block "+blk(101, 0).String()+" does not link to it")
	appendOK(t, l, finalize(blk(102, 0)))
	wantRejected(t, l, finalize(blk(101, 0)), "finalize to "+blk(101, 0).String()+": below finalized "+blk(102, 0).String())
	wantRejected(t, l, finalize(blk(102, 1)), "finalize to "+blk(102, 1).String()+": conflicts with finalized "+blk(102, 0).String())
	wantRejected(t, l, finalize(blk(103, 1)), "finalize to "+blk(103, 1).String()+": conflicts with window block "+blk(103, 0).String())
	child := fork(105, 0, 1)
	wantRejected(t, l, finalize(child), "finalize to "+child.String()+": parent "+child.Parent.Hex()+" is not tip hash "+blk(104, 0).Hash.Hex())

	before := viewOf(state(t, l))
	appendOK(t, l, finalize(blk(102, 0)))
	wantView(t, l, before)
	appendOK(t, l, finalize(blk(110, 0)))
	wantView(t, l, view{tip: blk(110, 0), fin: blk(110, 0)})

	empty := fresh()
	appendOK(t, empty, finalize(blk(100, 0)))
	wantView(t, empty, view{tip: blk(100, 0), fin: blk(100, 0)})
}

func testRule5FinalBlocks(t *testing.T, l chainlog.Log, fresh func() chainlog.Log) {
	appendOK(t, l, blocks(101, 104)...)
	wantRejected(t, l, finalBlock(blk(105, 0)), "block 105: final block with 4 unfinalized blocks in the window")

	b := fresh()
	appendOK(t, b, finalize(blk(100, 0)))
	for _, n := range []uint64{103, 105} {
		appendOK(t, b, finalBlock(blk(n, 0)))
		wantView(t, b, view{tip: blk(n, 0), fin: blk(n, 0)})
	}
	appendOK(t, b, blocks(106, 107)...)
	wantRejected(t, b, revert(blk(103, 0)), "revert to "+blk(103, 0).String()+": target is neither the finalized block nor in the window")
	appendOK(t, b, revert(blk(105, 0)))
	wantView(t, b, view{tip: blk(105, 0), fin: blk(105, 0)})
}

func testRule6Window(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	appendOK(t, l, blocks(101, 100+WindowCap)...)
	next := uint64(101 + WindowCap)
	wantRejected(t, l, block(blk(next, 0)), fmt.Sprintf("block %d: window is full (%d blocks)", next, WindowCap))
	appendOK(t, l, finalize(blk(102, 0)))
	appendOK(t, l, block(blk(next, 0)))
}

func testShape(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	r := blk(101, 0)
	lg := func(index, txIndex uint32) chain.Log {
		return chain.Log{BlockNumber: r.Number, BlockHash: r.Hash, Index: index, TxIndex: txIndex}
	}
	withLogs := func(logs ...chain.Log) chainlog.Entry {
		return chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r, Logs: logs}}
	}
	withTo := block(r)
	withTo.To = blk(99, 0)
	revertFinal := revert(blk(99, 0))
	revertFinal.Final = true
	finalizeLogs := finalize(blk(99, 0))
	finalizeLogs.Block.Logs = []chain.Log{lg(0, 0)}
	wrongNumber := lg(0, 0)
	wrongNumber.BlockNumber = 102
	wrongHash := lg(0, 0)
	wrongHash.BlockHash = blk(102, 0).Hash
	five := lg(0, 0)
	for range 5 {
		five.Topics = append(five.Topics, [32]byte{1})
	}
	tests := []struct {
		name   string
		e      chainlog.Entry
		detail string
	}{
		{"unknown kind", chainlog.Entry{Kind: 7, Block: chain.Block{Ref: r}}, "unknown kind 7"},
		{"block with To", withTo, "block 101: To must be zero"},
		{"revert with Final", revertFinal, "revert entry: Block and Final must be zero"},
		{"finalize with logs", finalizeLogs, "finalize entry: Block and Final must be zero"},
		{"block with zero hash", block(chain.BlockRef{Number: 101}), "block 101: zero hash"},
		{"revert to zero hash", revert(chain.BlockRef{Number: 99}), "revert to 99: zero hash"},
		{"log block number", withLogs(wrongNumber), "block 101: log 0: block number 102"},
		{"log block hash", withLogs(wrongHash), "block 101: log 0: block hash " + blk(102, 0).Hash.Hex()},
		{"log topics", withLogs(five), "block 101: log 0: 5 topics, at most 4"},
		{"log index", withLogs(lg(3, 0), lg(3, 0)), "block 101: log 1: index 3 not above 3"},
		{"log tx index", withLogs(lg(0, 2), lg(1, 1)), "block 101: log 1: tx index 1 below 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantRejected(t, l, tt.e, tt.detail)
		})
	}
}

func testTooLarge(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	r := blk(101, 0)
	big := chain.Log{BlockNumber: r.Number, BlockHash: r.Hash, Data: make([]byte, chainlog.MaxRecordSize)}
	e := chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: r, Logs: []chain.Log{big}}}
	sized := e
	sized.Offset = math.MaxUint64
	body := len(chainlog.Encode(nil, sized)) - 8
	wantRejected(t, l, e, fmt.Sprintf("block 101: record body of %d bytes exceeds 16777216", body))
	wantView(t, l, view{})
}

func testBatchAtomic(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	appendOK(t, l, block(blk(101, 0)))
	before := viewOf(state(t, l))
	_, err := l.Append(t.Context(), block(blk(102, 0)), block(blk(102, 0)), block(blk(103, 0)))
	wantErr(t, "Append", err, chainlog.ErrMalformed, "chainlog: append entry 2 of 3: chainlog: malformed entry: block 102 is not above tip "+blk(102, 0).String())
	eq(t, "Head", head(t, l), chainlog.Offset(1))
	wantView(t, l, before)
	wantEntries(t, read(t, l, 1, 10), numbered(block(blk(101, 0))))
}

func testStateCopy(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	appendOK(t, l, blocks(101, 103)...)
	want := view{tip: blk(103, 0), win: refs(blk(101, 0), blk(102, 0), blk(103, 0))}
	// The revert first: the block then writes into whatever array the copy's window shares.
	s := state(t, l)
	s.Apply(revert(blk(101, 0)))
	s.Apply(block(blk(102, 3)))
	w := state(t, l).Window()
	w[0] = chain.BlockRef{}
	w = append(w, blk(9, 9))
	w[1] = w[len(w)-1]
	wantView(t, l, want)
}

func testWait(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	ctx := t.Context()
	appendOK(t, l, blocks(101, 102)...)
	eq(t, "Wait(1) with Head 2", l.Wait(ctx, 1), error(nil))

	done := blockedWait(t, l, ctx, 2)
	appendOK(t, l, block(blk(103, 0)))
	eq(t, "blocked Wait(2) after an Append", receive(t, done, "Wait(2)"), error(nil))

	cctx, cancel := context.WithCancel(ctx)
	done = blockedWait(t, l, cctx, 3)
	cancel()
	wantErr(t, "blocked Wait(3) after cancel", receive(t, done, "Wait(3)"), context.Canceled, "")

	wantErr(t, "Wait(10) with Head 3", l.Wait(ctx, 10), chainlog.ErrAhead, "chainlog: offset beyond head: wait after 10, head is 3")

	const waiters = 16
	results := make([]<-chan error, waiters)
	for i := range results {
		results[i] = blockedWait(t, l, ctx, 3)
	}
	appendOK(t, l, block(blk(104, 0)))
	for _, done := range results {
		eq(t, "blocked waiter after one Append", receive(t, done, "Wait(3)"), error(nil))
	}
}

// waitLimit bounds every wait of the suite, so a broken log fails a subtest instead of hanging.
const waitLimit = 30 * time.Second

// signalCtx is a context that reports, once, that a caller asked for its Done channel. A Wait that
// blocks asks for it.
type signalCtx struct {
	context.Context
	asked chan struct{}
	once  sync.Once
}

func (c *signalCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.asked) })
	return c.Context.Done()
}

// blockedWait starts Wait(ctx, after) and returns its result channel once Wait is blocked, that
// is, once it asked for ctx.Done.
func blockedWait(t *testing.T, l chainlog.Log, ctx context.Context, after chainlog.Offset) <-chan error {
	t.Helper()
	sc := &signalCtx{Context: ctx, asked: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- l.Wait(sc, after) }()
	select {
	case <-sc.asked:
	case err := <-done:
		t.Fatalf("Wait(%d) returned %v instead of blocking", after, err)
	case <-time.After(waitLimit):
		t.Fatalf("Wait(%d) neither blocked nor returned within %v", after, waitLimit)
	}
	return done
}

// receive returns the result of a waiter, failing the test when it takes longer than waitLimit.
func receive(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(waitLimit):
		t.Fatalf("%s did not return within %v", what, waitLimit)
		return nil
	}
}

func testTruncate(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	ctx := t.Context()
	in := append(blocks(101, 106), finalize(blk(104, 0)))
	in = append(in, blocks(107, 108)...)
	all := numbered(in...)
	appendOK(t, l, in...)
	meta, err := l.Meta(ctx)
	noErr(t, err, "Meta")
	before := viewOf(state(t, l))

	for _, b := range []chainlog.Offset{0, 1} {
		noErr(t, l.Truncate(ctx, b), fmt.Sprintf("Truncate(%d)", b))
		eq(t, fmt.Sprintf("Oldest after Truncate(%d)", b), oldest(t, l), chainlog.Offset(1))
	}
	noErr(t, l.Truncate(ctx, 100), "Truncate(100)")
	o := oldest(t, l)
	// The newest Finalize is offset 7, so offsets 8 and 9 stay.
	eq(t, "1 <= Oldest after Truncate(100) <= 8", o >= 1 && o <= 8, true)
	wantEntries(t, read(t, l, o, 100), all[o-1:])
	if o > 1 {
		_, err := l.Read(ctx, 1, 1)
		wantErr(t, "Read(1, 1)", err, chainlog.ErrTruncated, fmt.Sprintf("chainlog: offset truncated: read from 1, oldest is %d", o))
	}
	eq(t, "Head", head(t, l), chainlog.Offset(9))
	wantView(t, l, before)
	after, err := l.Meta(ctx)
	noErr(t, err, "Meta")
	eq(t, "Meta after Truncate", after, meta)
	eq(t, "Append after Truncate", appendOK(t, l, block(blk(109, 0))), chainlog.Offset(10))
	eq(t, "Oldest after Append", oldest(t, l), o)
}

func testTruncateBound(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	appendOK(t, l, blocks(101, 105)...)
	noErr(t, l.Truncate(t.Context(), 10), "Truncate(10)")
	eq(t, "Oldest without a Finalize", oldest(t, l), chainlog.Offset(1))
	wantEntries(t, read(t, l, 1, 10), numbered(blocks(101, 105)...))
}

func testClosed(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	ctx := t.Context()
	appendOK(t, l, block(blk(101, 0)))
	done := blockedWait(t, l, ctx, 1)
	noErr(t, l.Close(), "Close")
	// The contract returns ErrClosed unwrapped, so compare with ==.
	eq(t, "Wait blocked before Close returns ErrClosed", receive(t, done, "Wait(1)") == chainlog.ErrClosed, true)
	for name, err := range calls(ctx, l) {
		eq(t, name+" after Close returns ErrClosed", err == chainlog.ErrClosed, true)
	}
	// Closed comes before a cancelled context and before the arguments.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for name, err := range calls(cancelled, l) {
		eq(t, name+" after Close with a cancelled context returns ErrClosed", err == chainlog.ErrClosed, true)
	}
	eq(t, "second Close", l.Close(), error(nil))
}

// calls calls every method of l except Close with ctx, in a fixed order, and yields each error.
func calls(ctx context.Context, l chainlog.Log) func(yield func(string, error) bool) {
	return func(yield func(string, error) bool) {
		_, err := l.Meta(ctx)
		if !yield("Meta", err) {
			return
		}
		_, err = l.Append(ctx, block(blk(102, 0)))
		if !yield("Append", err) {
			return
		}
		_, err = l.Read(ctx, 1, 1)
		if !yield("Read", err) {
			return
		}
		if !yield("Wait", l.Wait(ctx, 0)) {
			return
		}
		_, err = l.Head(ctx)
		if !yield("Head", err) {
			return
		}
		_, err = l.Oldest(ctx)
		if !yield("Oldest", err) {
			return
		}
		_, err = l.State(ctx)
		if !yield("State", err) {
			return
		}
		if !yield("Truncate", l.Truncate(ctx, 1)) {
			return
		}
		// Arguments are checked after the closed log and the context.
		_, err = l.Append(ctx)
		if !yield("Append()", err) {
			return
		}
		_, err = l.Append(ctx, chainlog.Entry{})
		if !yield("Append(malformed)", err) {
			return
		}
		if !yield("Wait(99)", l.Wait(ctx, 99)) {
			return
		}
		yield("Read(1, 0)", readRecovered(ctx, l))
	}
}

// readRecovered calls Read(ctx, 1, 0) and turns its panic into an error.
func readRecovered(ctx context.Context, l chainlog.Log) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	_, err = l.Read(ctx, 1, 0)
	return err
}

func testCancelledContext(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	appendOK(t, l, block(blk(101, 0)))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, err := range calls(ctx, l) {
		// The contract returns ctx.Err() unwrapped, so compare with ==.
		eq(t, name+" with a cancelled context returns context.Canceled", err == context.Canceled, true)
	}
	eq(t, "Head after an Append with a cancelled context", head(t, l), chainlog.Offset(1))
}

func testConcurrent(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	const first, last = 101, 2100 // 2,000 linked blocks
	var batches [][]chainlog.Entry
	var want []chainlog.Entry
	window := 0
	for n, i := uint64(first), 0; n <= last; i++ {
		end := min(n+uint64(i%3), last) // batch sizes cycle 1, 2, 3
		b := blocks(n, end)
		window += len(b)
		batches = append(batches, b)
		n = end + 1
		if window >= 5 {
			batches = append(batches, []chainlog.Entry{finalize(blk(end, 0))})
			window = 0
		}
	}
	for _, b := range batches {
		want = append(want, b...)
	}
	want = numbered(want...)
	total := chainlog.Offset(len(want)) //nolint:gosec // a few thousand entries

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const readers = 8
	got := make([][]chainlog.Entry, readers)
	errs := make([]error, readers)
	var wg sync.WaitGroup
	for r := range readers {
		wg.Go(func() { got[r], errs[r] = follow(ctx, l, total) })
	}
	var appendErr error
	for _, b := range batches {
		if _, appendErr = l.Append(ctx, b...); appendErr != nil {
			cancel() // stop the readers
			break
		}
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(waitLimit):
		cancel()
		t.Fatalf("readers did not finish within %v", waitLimit)
	}
	noErr(t, appendErr, "Append")
	for r := range readers {
		noErr(t, errs[r], "reader")
		eq(t, "a reader's entries", got[r], want)
	}
}

// follow reads l with Wait and Read(from, 7), as a consumer does, until it holds offset last.
func follow(ctx context.Context, l chainlog.Log, last chainlog.Offset) ([]chainlog.Entry, error) {
	var got []chainlog.Entry
	for from := chainlog.Offset(1); from <= last; {
		if err := l.Wait(ctx, from-1); err != nil {
			return got, err
		}
		es, err := l.Read(ctx, from, 7)
		if err != nil {
			return got, err
		}
		got = append(got, es...)
		from += chainlog.Offset(len(es)) //nolint:gosec // at most 7
	}
	return got, nil
}

func testBatchVisibility(t *testing.T, l chainlog.Log, _ func() chainlog.Log) {
	const batches = 300
	ctx := t.Context()
	appended := make(chan error, 1)
	go func() {
		for i := range uint64(batches) {
			a, b := blk(101+2*i, 0), blk(102+2*i, 0)
			if _, err := l.Append(ctx, block(a), block(b), finalize(b)); err != nil {
				appended <- err
				return
			}
		}
		appended <- nil
	}()
	var inside chainlog.Offset // a Head that is not a multiple of 3, if any
	deadline := time.Now().Add(waitLimit)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("Head did not reach %d within %v", 3*batches, waitLimit)
		}
		h, err := l.Head(ctx)
		noErr(t, err, "Head")
		if h%3 != 0 {
			inside = h
			break
		}
		if h == 3*batches {
			break
		}
		select {
		case err := <-appended:
			noErr(t, err, "Append")
			appended = nil // keep polling: Head reaches 900
		default:
		}
	}
	eq(t, "a Head inside a batch of 3", inside, chainlog.Offset(0))
}
