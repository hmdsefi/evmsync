// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"encoding/hex"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"
)

// nonZero returns the bloom's non-zero bytes by index.
func nonZero(b Bloom) map[int]byte {
	m := map[int]byte{}
	for i, v := range b {
		if v != 0 {
			m[i] = v
		}
	}
	return m
}

// bloomVector returns the bloom of usdc with topics transfer and one32.
func bloomVector() Bloom {
	return BloomOf([]Log{{Address: usdc, Topics: []common.Hash{transfer, one32}}})
}

func TestBloomVectors(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		prefix string
		want   map[int]byte
	}{
		{"empty", nil, "c5d2460186f7", map[int]byte{33: 0x80, 63: 0x02, 69: 0x04}},
		{"usdc", usdc[:], "80e7ec40fbf0", map[int]byte{119: 0x01, 129: 0x01, 227: 0x80}},
		{"transfer", transfer[:], "ada389e1fc24", map[int]byte{75: 0x08, 123: 0x10, 195: 0x02}},
		{"address 0x01", other[:], "146828805631", map[int]byte{57: 0x02, 114: 0x01, 239: 0x01}},
		{"zero32", zero32[:], "290decd9548b", map[int]byte{100: 0x02, 110: 0x08, 222: 0x20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := sha3.NewLegacyKeccak256()
			h.Write(tt.data)
			if got := hex.EncodeToString(h.Sum(nil)[:6]); got != tt.prefix {
				t.Fatalf("Keccak-256 prefix = %s, want %s", got, tt.prefix)
			}
			var b Bloom
			b.Add(tt.data)
			if got := nonZero(b); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("non-zero bytes after Add = %v, want %v", got, tt.want)
			}
			if !b.Test(tt.data) {
				t.Error("Test after Add = false, want true")
			}
		})
	}

	b := bloomVector()
	want := map[int]byte{
		61: 0x04, 75: 0x08, 85: 0x04, 119: 0x01, 123: 0x10,
		129: 0x01, 195: 0x02, 222: 0x40, 227: 0x80,
	}
	if got := nonZero(b); !reflect.DeepEqual(got, want) {
		t.Errorf("BloomOf non-zero bytes = %v, want %v", got, want)
	}
	tests2 := []struct {
		name string
		data []byte
		want bool
	}{
		{"usdc", usdc[:], true},
		{"transfer", transfer[:], true},
		{"one32", one32[:], true},
		{"zero32", zero32[:], false},
	}
	for _, tt := range tests2 {
		if got := b.Test(tt.data); got != tt.want {
			t.Errorf("BloomOf(...).Test(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
	if BloomOf(nil) != (Bloom{}) {
		t.Error("BloomOf(nil) is not the zero bloom")
	}
}

func TestBloomTestNeedsAllBits(t *testing.T) {
	// usdc sets bit masks 0x01, 0x01 and 0x80 in bytes 119, 129 and 227.
	for _, idx := range []int{119, 129, 227} {
		var b Bloom
		b.Add(usdc[:])
		b[idx] = 0
		if b.Test(usdc[:]) {
			t.Errorf("Test(usdc) with byte %d cleared = true, want false", idx)
		}
	}
}

func TestMayContain(t *testing.T) {
	b := bloomVector()
	tests := []struct {
		name  string
		bloom Bloom
		f     Filter
		want  bool
	}{
		{"zero filter", b, Filter{}, true},
		{"address", b, Filter{Addresses: []common.Address{usdc}}, true},
		{"other address", b, Filter{Addresses: []common.Address{other}}, false},
		{"other or address", b, Filter{Addresses: []common.Address{other, usdc}}, true},
		{"topic", b, Filter{Topics: [][]common.Hash{{transfer}}}, true},
		{"topic in position 1", b, Filter{Topics: [][]common.Hash{nil, {one32}}}, true},
		{"absent topic", b, Filter{Topics: [][]common.Hash{{zero32}}}, false},
		{"absent or present topic", b, Filter{Topics: [][]common.Hash{{zero32, transfer}}}, true},
		{"four wildcards", b, Filter{Topics: [][]common.Hash{nil, nil, nil, nil}}, true},
		{
			"address and absent topic",
			b,
			Filter{Addresses: []common.Address{usdc}, Topics: [][]common.Hash{{zero32}}},
			false,
		},
		{"zero bloom, zero filter", Bloom{}, Filter{}, true},
		{"zero bloom, address", Bloom{}, Filter{Addresses: []common.Address{usdc}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bloom.MayContain(tt.f); got != tt.want {
				t.Errorf("MayContain = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsZero(t *testing.T) {
	if !(Bloom{}).IsZero() {
		t.Error("Bloom{}.IsZero() = false, want true")
	}
	for _, i := range []int{0, 255} {
		var b Bloom
		b[i] = 1
		if b.IsZero() {
			t.Errorf("bloom with byte %d set: IsZero() = true, want false", i)
		}
	}
}

func TestBloomNoFalseNegatives(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed seed for reproducible cases, not for secrets
	addrs := []common.Address{usdc, other, common.HexToAddress("0x02"), common.HexToAddress("0x03")}
	topics := []common.Hash{transfer, one32, zero32, topA, topB, topX}
	matched := 0
	for range 10_000 {
		logs := make([]Log, 1+r.IntN(5))
		for i := range logs {
			logs[i].Address = addrs[r.IntN(len(addrs))]
			for range r.IntN(5) {
				logs[i].Topics = append(logs[i].Topics, topics[r.IntN(len(topics))])
			}
		}
		var f Filter
		for range r.IntN(3) {
			f.Addresses = append(f.Addresses, addrs[r.IntN(len(addrs))])
		}
		for range r.IntN(5) {
			var pos []common.Hash
			for range r.IntN(3) {
				pos = append(pos, topics[r.IntN(len(topics))])
			}
			f.Topics = append(f.Topics, pos)
		}
		for _, l := range logs {
			if f.Match(l) {
				matched++
				if !BloomOf(logs).MayContain(f) {
					t.Fatalf("false negative: logs %v, filter %v", logs, f)
				}
				break
			}
		}
	}
	if matched == 0 {
		t.Fatal("no generated case matched; the test checks nothing")
	}
}
