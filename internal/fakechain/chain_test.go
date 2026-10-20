// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package fakechain

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

func TestFixedHashes(t *testing.T) {
	genesis := common.HexToHash("0xe8f558fba9e9da77d722ed1ebf6ba8cfab9cbcad9c02b7956519c654125cbee2")
	for _, seed := range []uint64{0, 1, 99} {
		g := New(seed, Params{}).Genesis()
		want := chain.Header{Ref: chain.BlockRef{Number: 0, Hash: genesis, Time: GenesisTime}}
		if g != want {
			t.Errorf("seed %d: Genesis() = %+v, want %+v", seed, g, want)
		}
	}

	c := New(1, Params{})
	refs := c.Mine(1)
	want1 := common.HexToHash("0x29a0a0981921f5e87dc1acc7b3daac8046cda355a5dc98a4f726c71b70741428")
	if len(refs) != 1 || refs[0].Hash != want1 {
		t.Fatalf("Mine(1) = %v, want block 1 %s", refs, want1.Hex())
	}

	wantTx := common.HexToHash("0x599a841fa5fe37f98b3d243880780724ce325688e4f206abf98500b6ed31a865")
	if got := txHash(1); got != wantTx {
		t.Errorf("txHash(1) = %s, want %s", got.Hex(), wantTx.Hex())
	}
	// The first fresh log the chain creates is tx serial 1.
	for c.Head().Ref.Number < 50 {
		blocks := c.CanonicalBlocks(chain.Filter{})
		if i := slices.IndexFunc(blocks, func(b chain.Block) bool { return len(b.Logs) > 0 }); i >= 0 {
			if got := blocks[i].Logs[0].TxHash; got != wantTx {
				t.Errorf("first log TxHash = %s, want %s", got.Hex(), wantTx.Hex())
			}
			break
		}
		c.Mine(1)
	}

	topics := []struct {
		sig  string
		want common.Hash
	}{
		{"Transfer(address,address,uint256)", TransferTopic},
		{"Approval(address,address,uint256)", ApprovalTopic},
		{"Other(uint256)", OtherTopic},
	}
	for _, tt := range topics {
		if got := keccak([]byte(tt.sig)); got != tt.want {
			t.Errorf("keccak(%q) = %s, want %s", tt.sig, got.Hex(), tt.want.Hex())
		}
	}

	meta := New(1, Params{}).Meta(DefaultFilter)
	wantID := chainlog.ID(common.FromHex("0x1624a058136b4172d71fe4203c66094f"))
	if meta.ID != wantID {
		t.Errorf("Meta.ID = %x, want %x", meta.ID, wantID)
	}
	if meta.ChainID != 31337 || meta.Start != 0 {
		t.Errorf("Meta = %+v, want ChainID 31337, Start 0", meta)
	}
	if !slices.Equal(meta.Filter.Addresses, DefaultFilter.Addresses) || len(meta.Filter.Topics) != 1 ||
		!slices.Equal(meta.Filter.Topics[0], DefaultFilter.Topics[0]) {
		t.Errorf("Meta.Filter = %+v, want DefaultFilter", meta.Filter)
	}
	if id := New(2, Params{}).Meta(DefaultFilter).ID; id == wantID {
		t.Error("Meta.ID does not depend on the seed")
	}
	if got := New(1, Params{ChainID: 5}).Meta(chain.Filter{}).ChainID; got != 5 {
		t.Errorf("Meta.ChainID = %d, want 5", got)
	}
}

func TestFixtures(t *testing.T) {
	for i := range 4 {
		a := Address(i)
		var want common.Address
		want[0], want[19] = 0x10, byte(i)
		if a != want {
			t.Errorf("Address(%d) = %s, want %s", i, a.Hex(), want.Hex())
		}
	}
	for j := range 8 {
		a := Account(j)
		var want common.Address
		want[0], want[19] = 0x20, byte(j)
		if a != want {
			t.Errorf("Account(%d) = %s, want %s", j, a.Hex(), want.Hex())
		}
	}
	wantPanic(t, "evmsync: fakechain: fixture index 4 out of range", func() { Address(4) })
	wantPanic(t, "evmsync: fakechain: fixture index -1 out of range", func() { Address(-1) })
	wantPanic(t, "evmsync: fakechain: fixture index 8 out of range", func() { Account(8) })
	wantPanic(t, "evmsync: fakechain: fixture index -1 out of range", func() { Account(-1) })

	for i := range 4 {
		l := chain.Log{Address: Address(i), Topics: []common.Hash{TransferTopic, {}, {}}}
		if got, want := DefaultFilter.Match(l), i < 2; got != want {
			t.Errorf("DefaultFilter.Match(Transfer of contract %d) = %v, want %v", i, got, want)
		}
	}
	for _, topic := range []common.Hash{ApprovalTopic, OtherTopic} {
		l := chain.Log{Address: Address(0), Topics: []common.Hash{topic}}
		if got, want := DefaultFilter.Match(l), topic == ApprovalTopic; got != want {
			t.Errorf("DefaultFilter.Match(%s) = %v, want %v", topic.Hex(), got, want)
		}
	}
}

func TestParams(t *testing.T) {
	want := Params{
		ChainID: 31337, ForkPPM: 150_000, MaxForkDepth: 8, MaxMine: 3, FinalityPPM: 500_000,
		MinLag: 2, MaxLag: 32, MoveLogPPM: 300_000,
	}
	if got := New(1, Params{}).Params(); got != want {
		t.Errorf("Params() = %+v, want %+v", got, want)
	}
	set := Params{
		ChainID: 1, ForkPPM: 2, MaxForkDepth: 3, MaxMine: 4, FinalityPPM: 5, MinLag: 6, MaxLag: 7, MoveLogPPM: 8,
	}
	if got := New(1, set).Params(); got != set {
		t.Errorf("Params() = %+v, want %+v", got, set)
	}
}

// TestFreshLogs derives block 1's logs from the logs stream exactly as the generator must draw
// them, and compares.
func TestFreshLogs(t *testing.T) {
	counts := []int{0, 0, 0, 1, 1, 1, 2, 2, 3, 4}
	for seed := uint64(1); seed <= 30; seed++ {
		r := NewRand(seed, "logs")
		var want []chain.Log
		tx := uint64(0)
		for range 3 { // blocks 1..3
			n := counts[r.Intn(10)]
			var logs []chain.Log
			for i := range n {
				a := int(r.Intn(4)) //nolint:gosec // below 4
				k := r.Intn(10)
				l := chain.Log{Address: Address(a), Index: uint32(i), TxIndex: uint32(i)}
				data := make([]byte, 32)
				if k <= 7 {
					topic := TransferTopic
					if k >= 6 {
						topic = ApprovalTopic
					}
					from, to := int(r.Intn(8)), int(r.Intn(8)) //nolint:gosec // below 8
					amount := 1 + r.Intn(1000)
					l.Topics = []common.Hash{topic, pad(Account(from)), pad(Account(to))}
					binary.BigEndian.PutUint64(data[24:], amount)
				} else {
					l.Topics = []common.Hash{OtherTopic}
					binary.BigEndian.PutUint64(data[24:], r.Uint64())
				}
				l.Data = data
				tx++
				l.TxHash = txHash(tx)
				logs = append(logs, l)
			}
			want = append(want, logs...)
		}
		c := New(seed, Params{})
		c.Mine(3)
		var got []chain.Log
		for _, b := range c.CanonicalBlocks(chain.Filter{}) {
			for i := range b.Logs {
				l := b.Logs[i]
				if l.BlockNumber != b.Ref.Number || l.BlockHash != b.Ref.Hash {
					t.Errorf("seed %d: log %d of block %d names block %d %s", seed, i, b.Ref.Number,
						l.BlockNumber, l.BlockHash.Hex())
				}
				l.BlockNumber, l.BlockHash = 0, common.Hash{}
				got = append(got, l)
			}
		}
		if !logsEqual(got, want) {
			t.Fatalf("seed %d: logs of blocks 1..3 =\n%+v\nwant\n%+v", seed, got, want)
		}
	}
}

func pad(a common.Address) common.Hash {
	var h common.Hash
	copy(h[12:], a[:])
	return h
}

func logsEqual(a, b []chain.Log) bool {
	return slices.EqualFunc(a, b, func(x, y chain.Log) bool {
		return x.Address == y.Address && slices.Equal(x.Topics, y.Topics) && slices.Equal(x.Data, y.Data) &&
			x.BlockNumber == y.BlockNumber && x.BlockHash == y.BlockHash && x.TxHash == y.TxHash &&
			x.TxIndex == y.TxIndex && x.Index == y.Index
	})
}

func TestBloom(t *testing.T) {
	b := chain.BloomOf([]chain.Log{{Address: Address(0), Topics: []common.Hash{TransferTopic}}})
	want := map[int]byte{53: 0x02, 73: 0x08, 75: 0x08, 123: 0x10, 176: 0x40, 195: 0x02}
	for i, v := range b {
		if v != want[i] {
			t.Errorf("bloom byte %d = %#x, want %#x", i, v, want[i])
		}
	}

	c := New(3, Params{})
	c.Mine(100)
	var withLogs, matching int
	for n := uint64(1); n <= 100; n++ {
		h, ok := c.Canonical(n)
		if !ok {
			t.Fatalf("Canonical(%d) missing", n)
		}
		all, _ := c.Block(h.Ref.Hash, chain.Filter{})
		if h.Bloom != chain.BloomOf(all.Logs) {
			t.Errorf("block %d: bloom is not the bloom of its logs", n)
		}
		if got, want := h.Bloom.IsZero(), len(all.Logs) == 0; got != want {
			t.Errorf("block %d: Bloom.IsZero() = %v, want %v", n, got, want)
		}
		sel, _ := c.Block(h.Ref.Hash, DefaultFilter)
		if len(sel.Logs) > 0 && !h.Bloom.MayContain(DefaultFilter) {
			t.Errorf("block %d: MayContain(DefaultFilter) = false with %d matching logs", n, len(sel.Logs))
		}
		if len(all.Logs) > 0 {
			withLogs++
		}
		if len(sel.Logs) > 0 {
			matching++
		}
	}
	if withLogs == 0 || withLogs == 100 || matching == 0 {
		t.Errorf("seed 3: %d blocks with logs, %d with matching logs; the check covers no case", withLogs, matching)
	}
}

func TestStepInvariants(t *testing.T) {
	var shorter, rollback, moved bool
	for seed := uint64(1); seed <= 200; seed++ {
		c := New(seed, Params{MaxForkDepth: 8, ForkPPM: 400_000})
		for i := range 500 {
			head, fin := c.Head().Ref.Number, c.Finalized().Ref.Number
			ev := c.Step()
			if ev.Kind == EventReorg {
				if ev.Depth > head || head-ev.Depth < fin {
					t.Fatalf("seed %d step %d: reorg depth %d from head %d forks below finalized %d",
						seed, i, ev.Depth, head, fin)
				}
				shorter = shorter || (ev.Added == int(ev.Depth)-1 && ev.Added > 0) //nolint:gosec // depth <= 8
				rollback = rollback || ev.Added == 0
			}
			if got := c.Finalized().Ref.Number; got < fin {
				t.Fatalf("seed %d step %d: finalized moved from %d down to %d", seed, i, fin, got)
			}
			if ev.Head != c.Head().Ref || ev.Finalized != c.Finalized().Ref.Number {
				t.Fatalf("seed %d step %d: event %v does not match head %v fin %d", seed, i, ev,
					c.Head().Ref, c.Finalized().Ref.Number)
			}
		}
		prev := c.Genesis().Ref
		for n := uint64(1); n <= c.Head().Ref.Number; n++ {
			h, _ := c.Canonical(n)
			if h.Ref.Parent != prev.Hash || h.Ref.Number != n || h.Ref.Time != GenesisTime+12*n {
				t.Fatalf("seed %d: canonical block %d is %+v after %s", seed, n, h.Ref, prev)
			}
			prev = h.Ref
		}
		if !moved {
			moved = movedLog(c)
		}
	}
	if !shorter || !rollback || !moved {
		t.Errorf("coverage: shorter branch %v, rollback %v, moved log %v; want all true", shorter, rollback, moved)
	}
}

// movedLog reports whether a TxHash appears in both an orphan and a canonical block.
func movedLog(c *Chain) bool {
	canon := map[common.Hash]bool{}
	for _, b := range c.CanonicalBlocks(chain.Filter{}) {
		for _, l := range b.Logs {
			canon[l.TxHash] = true
		}
	}
	for h, nd := range c.nodes {
		if !c.Orphaned(h) {
			continue
		}
		for _, l := range nd.logs {
			if canon[l.TxHash] {
				return true
			}
		}
	}
	return false
}

func TestExplicitAPI(t *testing.T) {
	c := New(1, Params{})
	g := c.Genesis().Ref
	refs := c.Mine(5)
	if len(refs) != 5 {
		t.Fatalf("Mine(5) returned %d refs", len(refs))
	}
	prev := g
	for i, r := range refs {
		if r.Number != uint64(i+1) || r.Parent != prev.Hash || r.Time != GenesisTime+12*r.Number {
			t.Errorf("Mine(5) ref %d = %+v after %+v", i, r, prev)
		}
		prev = r
	}
	if c.Head().Ref != refs[4] {
		t.Errorf("Head() = %v, want %v", c.Head().Ref, refs[4])
	}

	re := c.Reorg(2, 3)
	if len(re) != 3 || re[0].Number != 4 || re[1].Number != 5 || re[2].Number != 6 {
		t.Fatalf("Reorg(2, 3) = %v, want blocks 4, 5, 6", re)
	}
	if re[0].Parent != refs[2].Hash || re[1].Parent != re[0].Hash || re[2].Parent != re[1].Hash {
		t.Errorf("Reorg(2, 3) refs %v are not linked to block 3 %s", re, refs[2].Hash.Hex())
	}
	for _, r := range refs[3:] {
		if !c.Orphaned(r.Hash) {
			t.Errorf("first-branch block %d not Orphaned", r.Number)
		}
		if h, ok := c.Header(r.Hash); !ok || h.Ref != r {
			t.Errorf("Header(orphan %d) = %v, %v", r.Number, h.Ref, ok)
		}
		if _, ok := c.Block(r.Hash, chain.Filter{}); !ok {
			t.Errorf("Block(orphan %d) not found", r.Number)
		}
	}
	for _, r := range append(refs[:3:3], re...) {
		if c.Orphaned(r.Hash) {
			t.Errorf("canonical block %d reported Orphaned", r.Number)
		}
		if h, ok := c.Canonical(r.Number); !ok || h.Ref != r {
			t.Errorf("Canonical(%d) = %v, %v, want %v", r.Number, h.Ref, ok, r)
		}
	}
	if c.Orphaned(common.Hash{1}) {
		t.Error("Orphaned(unknown) = true")
	}
	if _, ok := c.Canonical(7); ok {
		t.Error("Canonical(7) above head found")
	}
	if _, ok := c.Header(common.Hash{1}); ok {
		t.Error("Header(unknown) found")
	}
	if _, ok := c.Block(common.Hash{1}, chain.Filter{}); ok {
		t.Error("Block(unknown) found")
	}

	wantPanic(t, "evmsync: fakechain: reorg depth 7 above head 6", func() { c.Reorg(7, 0) })
	wantPanic(t, "evmsync: fakechain: negative block count", func() { c.Reorg(1, -1) })
	wantPanic(t, "evmsync: fakechain: negative block count", func() { c.Mine(-1) })
	wantPanic(t, "evmsync: fakechain: finalized 7 outside [0, 6]", func() { c.SetFinalized(7) })
	c.SetFinalized(2)
	if c.Finalized().Ref != refs[1] {
		t.Errorf("Finalized() = %v, want block 2", c.Finalized().Ref)
	}
	wantPanic(t, "evmsync: fakechain: finalized 1 outside [2, 6]", func() { c.SetFinalized(1) })
	if got := c.Reorg(0, 0); len(got) != 0 || c.Head().Ref != re[2] {
		t.Errorf("Reorg(0, 0) = %v, head %v; want no change", got, c.Head().Ref)
	}

	low := c.Reorg(6, 1)
	if len(low) != 1 || low[0].Number != 1 || low[0].Parent != g.Hash {
		t.Fatalf("Reorg(6, 1) = %v, want one block 1 on genesis", low)
	}
	if got := c.Finalized().Ref; got != low[0] {
		t.Errorf("after Reorg(6, 1) Finalized() = %v, want new block 1", got)
	}

	// Returned values are copies.
	c = New(4, Params{})
	c.Mine(30)
	var h common.Hash
	for _, b := range c.CanonicalBlocks(chain.Filter{}) {
		if len(b.Logs) > 0 {
			h = b.Ref.Hash
			break
		}
	}
	first, _ := c.Block(h, chain.Filter{})
	keep, _ := c.Block(h, chain.Filter{})
	first.Logs[0].Topics[0] = common.Hash{9}
	first.Logs[0].Data[31] ^= 0xff
	first.Logs[0].Address = common.Address{9}
	first.Logs = first.Logs[:0]
	second, _ := c.Block(h, chain.Filter{})
	if !logsEqual(second.Logs, keep.Logs) {
		t.Error("mutating a returned Block changed a later Block result")
	}
	blocks := c.CanonicalBlocks(chain.Filter{})
	for i := range blocks {
		for j := range blocks[i].Logs {
			blocks[i].Logs[j].Topics[0] = common.Hash{9}
		}
		blocks[i].Logs = nil
	}
	again, _ := c.Block(h, chain.Filter{})
	if !logsEqual(again.Logs, keep.Logs) {
		t.Error("mutating CanonicalBlocks changed a later Block result")
	}
}

func TestBlockFilter(t *testing.T) {
	c := New(6, Params{})
	c.Mine(40)
	var none, some int
	for _, b := range c.CanonicalBlocks(DefaultFilter) {
		all, _ := c.Block(b.Ref.Hash, chain.Filter{})
		var want []chain.Log
		for _, l := range all.Logs {
			if DefaultFilter.Match(l) {
				want = append(want, l)
			}
		}
		if !logsEqual(b.Logs, want) {
			t.Errorf("block %d: CanonicalBlocks(DefaultFilter) logs %v, want %v", b.Ref.Number, b.Logs, want)
		}
		if len(want) == 0 {
			none++
			if b.Logs != nil {
				t.Errorf("block %d: Logs = %v, want nil", b.Ref.Number, b.Logs)
			}
		} else {
			some++
		}
		got, _ := c.Block(b.Ref.Hash, DefaultFilter)
		if got.Ref != b.Ref || !logsEqual(got.Logs, want) {
			t.Errorf("block %d: Block(DefaultFilter) = %+v, want %+v", b.Ref.Number, got, b)
		}
	}
	if none == 0 || some == 0 {
		t.Errorf("seed 6: %d blocks without and %d with matching logs; the check covers no case", none, some)
	}
	if n := len(c.CanonicalBlocks(DefaultFilter)); n != 40 {
		t.Errorf("CanonicalBlocks returned %d blocks, want 40", n)
	}
}

func TestReorgMovesLogs(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		c := New(seed, Params{MoveLogPPM: 1_000_000})
		c.Mine(6)
		var pool []chain.Log
		for n := uint64(4); n <= 6; n++ {
			h, _ := c.Canonical(n)
			b, _ := c.Block(h.Ref.Hash, chain.Filter{})
			pool = append(pool, b.Logs...)
		}
		c.Reorg(3, 2)
		var gotMoved []chain.Log
		for n := uint64(4); n <= 5; n++ {
			h, _ := c.Canonical(n)
			b, _ := c.Block(h.Ref.Hash, chain.Filter{})
			fresh := true
			for i, l := range b.Logs {
				if l.Index != uint32(i) || l.TxIndex != uint32(i) {
					t.Fatalf("seed %d block %d: log %d has Index %d TxIndex %d", seed, n, i, l.Index, l.TxIndex)
				}
				isMoved := slices.ContainsFunc(pool, func(p chain.Log) bool { return p.TxHash == l.TxHash })
				if !isMoved && !fresh {
					t.Fatalf("seed %d block %d: fresh log %d after a moved log", seed, n, i)
				}
				if isMoved {
					fresh = false
					gotMoved = append(gotMoved, l)
				}
			}
		}
		if len(gotMoved) != len(pool) {
			t.Fatalf("seed %d: %d logs moved, want all %d (MoveLogPPM 1,000,000)", seed, len(gotMoved), len(pool))
		}
		// Moved logs keep their content and pool order within each block.
		for _, l := range gotMoved {
			i := slices.IndexFunc(pool, func(p chain.Log) bool { return p.TxHash == l.TxHash })
			p := pool[i]
			if p.Address != l.Address || !slices.Equal(p.Topics, l.Topics) || !slices.Equal(p.Data, l.Data) {
				t.Errorf("seed %d: moved log %s changed content", seed, l.TxHash.Hex())
			}
		}
	}

	// A rollback drops the pool with no target draw: the logs stream advances by one Chance per
	// orphaned log only.
	c := New(2, Params{})
	c.Mine(8)
	orphaned := 0
	for n := uint64(5); n <= 8; n++ {
		h, _ := c.Canonical(n)
		b, _ := c.Block(h.Ref.Hash, chain.Filter{})
		orphaned += len(b.Logs)
	}
	twin := *c.logRand
	c.Reorg(4, 0)
	for range orphaned {
		twin.Uint64()
	}
	if c.logRand.Uint64() != twin.Uint64() {
		t.Errorf("Reorg(4, 0) over %d orphaned logs drew a different number of values", orphaned)
	}
	if orphaned == 0 {
		t.Error("seed 2: no orphaned logs; the rollback check covers no case")
	}
}

func TestDeterminism(t *testing.T) {
	run := func() string {
		c := New(11, Params{ForkPPM: 400_000})
		var evs []Event
		for range 300 {
			evs = append(evs, c.Step())
		}
		c.Reorg(2, 1)
		c.Mine(2)
		return chainDigest(c, evs)
	}
	if a, b := run(), run(); a != b {
		t.Errorf("same seed and calls gave digests %s and %s", a, b)
	}
}

func TestEventString(t *testing.T) {
	h := common.HexToHash("0x1234abcd00000000000000000000000000000000000000000000000000000000")
	e := Event{Kind: EventMine, Added: 2, Head: chain.BlockRef{Number: 41, Hash: h}, Finalized: 30}
	if got, want := e.String(), "mine 2 -> head 41 0x1234abcd fin 30"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	h2 := common.HexToHash("0x9876fedc00000000000000000000000000000000000000000000000000000000")
	e = Event{Kind: EventReorg, Depth: 3, Added: 4, Head: chain.BlockRef{Number: 42, Hash: h2}, Finalized: 30}
	if got, want := e.String(), "reorg depth 3 +4 -> head 42 0x9876fedc fin 30"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestStepDraws checks the Step draw order of TST-120 against a model on the chain stream.
func TestStepDraws(t *testing.T) {
	for seed := uint64(1); seed <= 50; seed++ {
		p := Params{ForkPPM: 400_000, MaxForkDepth: 5, MaxMine: 4, FinalityPPM: 600_000, MinLag: 1, MaxLag: 6}
		c := New(seed, p)
		r := NewRand(seed, "chain")
		for i := range 200 {
			head, fin := c.Head().Ref.Number, c.Finalized().Ref.Number
			want := Event{}
			if r.Chance(p.ForkPPM) && head > fin {
				maxD := min(p.MaxForkDepth, head-fin)
				d := uint64(1)
				for d < maxD && r.Chance(500_000) {
					d++
				}
				n := int(d - 1 + r.Intn(3)) //nolint:gosec // at most MaxForkDepth + 1
				want = Event{Kind: EventReorg, Depth: d, Added: n}
				head = head - d + uint64(n)
			} else {
				n := int(1 + r.Intn(p.MaxMine)) //nolint:gosec // at most MaxMine
				want = Event{Kind: EventMine, Added: n}
				head += uint64(n) //nolint:gosec // n >= 1
			}
			adv := r.Chance(p.FinalityPPM)
			lag := p.MinLag + r.Intn(p.MaxLag-p.MinLag+1)
			if adv && head >= lag && head-lag > fin {
				fin = head - lag
			}
			got := c.Step()
			if got.Kind != want.Kind || got.Depth != want.Depth || got.Added != want.Added ||
				got.Head.Number != head || got.Finalized != fin {
				t.Fatalf("seed %d step %d: Step() = %v, want kind %d depth %d added %d head %d fin %d",
					seed, i, got, want.Kind, want.Depth, want.Added, head, fin)
			}
		}
	}
}
