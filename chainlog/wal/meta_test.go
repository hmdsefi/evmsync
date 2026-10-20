// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

// seal returns body with the meta and checkpoint file header (length, CRC-32C) in front.
func seal(body []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(body))) //nolint:gosec // test bodies are small
	b = binary.LittleEndian.AppendUint32(b, crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)))
	return append(b, body...)
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func uv(x uint64) []byte { return binary.AppendUvarint(nil, x) }

func rep(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

// testMeta has two addresses and three topic positions, the middle one empty.
func testMeta() chainlog.Meta {
	m := chainlog.Meta{ID: chainlog.ID{0: 0x11, 15: 0xff}, ChainID: 31337, Start: 300}
	m.Filter.Addresses = append(m.Filter.Addresses, [20]byte{19: 0xaa}, [20]byte{0: 0xbb})
	m.Filter.Topics = append(m.Filter.Topics, nil, nil, nil)
	m.Filter.Topics[0] = append(m.Filter.Topics[0], [32]byte{31: 1}, [32]byte{31: 2})
	m.Filter.Topics[1] = m.Filter.Topics[1][:0:0] // empty, not nil
	m.Filter.Topics[2] = append(m.Filter.Topics[2], [32]byte{0: 3})
	return m
}

func TestMetaRoundTrip(t *testing.T) {
	m := testMeta()
	got, err := decodeMeta(encodeMeta(m))
	if err != nil {
		t.Fatalf("decodeMeta: %v", err)
	}
	want := testMeta()
	want.Filter.Topics[1] = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeMeta(encodeMeta(m)) =\n%+v\nwant\n%+v", got, want)
	}
	if got.Filter.Topics[1] != nil {
		t.Errorf("empty topic position decoded as %#v, want nil", got.Filter.Topics[1])
	}

	empty := chainlog.Meta{ID: chainlog.ID{1}, Filter: chain.Filter{Addresses: testMeta().Filter.Addresses[:0], Topics: testMeta().Filter.Topics[:0]}}
	got, err = decodeMeta(encodeMeta(empty))
	if err != nil {
		t.Fatalf("decodeMeta: %v", err)
	}
	if got.Filter.Addresses != nil || got.Filter.Topics != nil || got.ID != empty.ID {
		t.Errorf("meta with empty lists decoded as %+v, want nil Addresses and Topics", got)
	}
}

// metaBody is the body encodeMeta writes for testMeta, built by hand.
func metaBody() []byte {
	m := testMeta()
	return cat(
		[]byte("EVMSMETA"), []byte{0x01}, m.ID[:], uv(31337), uv(300),
		uv(2), m.Filter.Addresses[0][:], m.Filter.Addresses[1][:],
		uv(3), uv(2), m.Filter.Topics[0][0][:], m.Filter.Topics[0][1][:], uv(0), uv(1), m.Filter.Topics[2][0][:],
	)
}

func TestMetaLayout(t *testing.T) {
	b := encodeMeta(testMeta())
	if want := seal(metaBody()); !bytes.Equal(b, want) {
		t.Fatalf("encodeMeta =\n%x\nwant\n%x", b, want)
	}
	id := testMeta().ID
	if string(b[8:16]) != "EVMSMETA" || b[16] != 0x01 || !bytes.Equal(b[17:33], id[:]) {
		t.Errorf("meta starts %x, want the header, EVMSMETA, 01 and the ID", b[:33])
	}
}

func TestMetaCorrupt(t *testing.T) {
	valid := encodeMeta(testMeta())
	body := metaBody()
	flipped := bytes.Clone(valid)
	flipped[20] ^= 0x01
	tooLarge := bytes.Clone(valid)
	binary.LittleEndian.PutUint32(tooLarge, 1048577)
	head := cat([]byte("EVMSMETA"), []byte{0x01}, make([]byte, 16), uv(1), uv(2)) // through start

	tests := []struct {
		name   string
		data   []byte
		detail string
	}{
		{"cut to 5 bytes", valid[:5], "truncated"},
		{"empty", nil, "truncated"},
		{"cut by one byte", valid[:len(valid)-1], "truncated"},
		{"flipped body byte", flipped, "checksum mismatch"},
		{"bad magic", seal(cat([]byte("XXXXXXXX"), body[8:])), "bad magic"},
		{"version 2", seal(cat(body[:8], []byte{0x02}, body[9:])), "version 2"},
		{"npos 5", seal(cat(head, uv(0), uv(5))), "filter out of range"},
		{"naddr 1001", seal(cat(head, uv(1001))), "filter out of range"},
		{"ntopics 1001", seal(cat(head, uv(0), uv(1), uv(1001))), "filter out of range"},
		{"length 1048577", tooLarge, "too large"},
		{"trailing byte in the body", seal(cat(body, []byte{0x00})), "trailing bytes"},
		{"trailing byte after the body", cat(valid, []byte{0x00}), "trailing bytes"},
		{"body cut in the ID", seal(body[:12]), "truncated"},
		{"body cut in an address", seal(cat(head, uv(1), rep(0xaa, 19))), "truncated"},
		{"body cut in a topic", seal(cat(head, uv(0), uv(1), uv(1), rep(0x01, 31))), "truncated"},
		{"chain ID non-minimal", seal(cat(body[:25], []byte{0x80, 0x00})), "truncated"},
		{"no positions count", seal(cat(head, uv(0))), "truncated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeMeta(tt.data)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("decodeMeta error = %v, want one wrapping ErrCorrupt", err)
			}
			if got, want := err.Error(), "wal: corrupt log: meta: "+tt.detail; got != want {
				t.Errorf("error = %q, want %q", got, want)
			}
		})
	}
}

func TestWriteFileAtomic(t *testing.T) {
	for _, sync := range []bool{false, true} {
		dir := t.TempDir()
		if err := writeFileAtomic(dir, "meta", []byte("first"), sync); err != nil {
			t.Fatalf("writeFileAtomic: %v", err)
		}
		if err := writeFileAtomic(dir, "meta", []byte("second"), sync); err != nil {
			t.Fatalf("writeFileAtomic over an existing file: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "meta"))
		if err != nil || string(got) != "second" {
			t.Errorf("sync %v: meta = %q, %v; want \"second\"", sync, got, err)
		}
		fi, err := os.Stat(filepath.Join(dir, "meta"))
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("sync %v: mode %v, want 0600", sync, mode)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Errorf("sync %v: directory holds %v, want only meta", sync, entries)
		}
	}

	missing := filepath.Join(t.TempDir(), "absent")
	err := writeFileAtomic(missing, "meta", []byte("x"), true)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("writeFileAtomic into a missing directory = %v, want one wrapping fs.ErrNotExist", err)
	}
	if want := "wal: open " + filepath.Join(missing, "meta.tmp") + ": "; len(err.Error()) <= len(want) || err.Error()[:len(want)] != want {
		t.Errorf("error = %q, want the prefix %q", err, want)
	}
	if err := syncDir(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("syncDir of a missing directory = %v, want one wrapping fs.ErrNotExist", err)
	}
}
