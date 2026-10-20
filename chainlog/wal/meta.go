// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"slices"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

const (
	metaName     = "meta"
	metaMagic    = "EVMSMETA"
	metaVersion  = 1
	maxMetaSize  = 1 << 20 // the largest meta body, in bytes
	fileHeader   = 8       // length:uint32le crc:uint32le
	addressLen   = 20
	hashLen      = 32
	maxFilterLen = chain.MaxFilterValues
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// encodeMeta returns the meta file for m: a header (body length and CRC-32C of the body, both
// little-endian uint32) and a body of the magic, version 1, the ID, the chain ID and start as
// varints, then the filter's addresses and topic positions, each list prefixed by its length.
// m's filter must already be valid.
func encodeMeta(m chainlog.Meta) []byte {
	b := make([]byte, fileHeader, 64)
	b = append(b, metaMagic...)
	b = append(b, metaVersion)
	b = append(b, m.ID[:]...)
	b = binary.AppendUvarint(b, m.ChainID)
	b = binary.AppendUvarint(b, m.Start)
	b = binary.AppendUvarint(b, uint64(len(m.Filter.Addresses)))
	for _, a := range m.Filter.Addresses {
		b = append(b, a[:]...)
	}
	b = binary.AppendUvarint(b, uint64(len(m.Filter.Topics)))
	for _, pos := range m.Filter.Topics {
		b = binary.AppendUvarint(b, uint64(len(pos)))
		for _, t := range pos {
			b = append(b, t[:]...)
		}
	}
	return sealFile(b)
}

// sealFile writes the length and CRC-32C of b[fileHeader:] into b's header and returns b.
func sealFile(b []byte) []byte {
	body := b[fileHeader:]
	binary.LittleEndian.PutUint32(b, uint32(len(body))) //nolint:gosec // meta and checkpoint bodies are far below 4 GiB
	binary.LittleEndian.PutUint32(b[4:], crc32.Checksum(body, crcTable))
	return b
}

// openFile checks the header of a meta or checkpoint file and returns its body. maxSize is the
// largest body the file may declare. Failures are details for ErrCorrupt.
func openFile(b []byte, maxSize uint32) ([]byte, string) {
	if len(b) < fileHeader {
		return nil, "truncated"
	}
	length := binary.LittleEndian.Uint32(b)
	if length > maxSize {
		return nil, "too large"
	}
	if uint64(len(b)) < fileHeader+uint64(length) {
		return nil, "truncated"
	}
	body := b[fileHeader : fileHeader+int(length)]
	if crc32.Checksum(body, crcTable) != binary.LittleEndian.Uint32(b[4:]) {
		return nil, "checksum mismatch"
	}
	if len(b) > fileHeader+int(length) {
		return nil, "trailing bytes"
	}
	return body, ""
}

// decodeMeta decodes a meta file into a normalized meta: empty filter lists are nil. Its errors
// wrap ErrCorrupt and read "wal: corrupt log: meta: <detail>".
func decodeMeta(b []byte) (chainlog.Meta, error) {
	corrupt := func(detail string) (chainlog.Meta, error) {
		return chainlog.Meta{}, fmt.Errorf("%w: meta: %s", ErrCorrupt, detail)
	}
	body, detail := openFile(b, maxMetaSize)
	if detail != "" {
		return corrupt(detail)
	}
	d := reader{b: body}
	magic, ok := d.take(len(metaMagic))
	if !ok {
		return corrupt("truncated")
	}
	if string(magic) != metaMagic {
		return corrupt("bad magic")
	}
	version, ok := d.take(1)
	if !ok {
		return corrupt("truncated")
	}
	if version[0] != metaVersion {
		return corrupt(fmt.Sprintf("version %d", version[0]))
	}
	var m chainlog.Meta
	id, ok := d.take(len(m.ID))
	if !ok {
		return corrupt("truncated")
	}
	copy(m.ID[:], id)
	if m.ChainID, ok = d.uvarint(); !ok {
		return corrupt("truncated")
	}
	if m.Start, ok = d.uvarint(); !ok {
		return corrupt("truncated")
	}

	naddr, ok := d.uvarint()
	if !ok {
		return corrupt("truncated")
	}
	if naddr > maxFilterLen {
		return corrupt("filter out of range")
	}
	if naddr > 0 {
		m.Filter.Addresses = slices.Grow(m.Filter.Addresses, int(naddr))
	}
	for range naddr {
		a, ok := d.take(addressLen)
		if !ok {
			return corrupt("truncated")
		}
		m.Filter.Addresses = append(m.Filter.Addresses, [addressLen]byte(a))
	}

	npos, ok := d.uvarint()
	if !ok {
		return corrupt("truncated")
	}
	if npos > chain.MaxTopicPositions {
		return corrupt("filter out of range")
	}
	if npos > 0 {
		m.Filter.Topics = slices.Grow(m.Filter.Topics, int(npos))
	}
	for i := range npos {
		ntopics, ok := d.uvarint()
		if !ok {
			return corrupt("truncated")
		}
		if ntopics > maxFilterLen {
			return corrupt("filter out of range")
		}
		m.Filter.Topics = append(m.Filter.Topics, nil)
		if ntopics > 0 {
			m.Filter.Topics[i] = slices.Grow(m.Filter.Topics[i], int(ntopics))
		}
		for range ntopics {
			t, ok := d.take(hashLen)
			if !ok {
				return corrupt("truncated")
			}
			m.Filter.Topics[i] = append(m.Filter.Topics[i], [hashLen]byte(t))
		}
	}
	if len(d.b) > 0 {
		return corrupt("trailing bytes")
	}
	return m, nil
}

// reader reads the fields of a meta or checkpoint body in order.
type reader struct {
	b []byte // the bytes not read yet
}

func (d *reader) take(n int) ([]byte, bool) {
	if n > len(d.b) {
		return nil, false
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p, true
}

// uvarint reads a minimal unsigned varint. It reports false for a cut, overlong or padded one.
func (d *reader) uvarint() (uint64, bool) {
	x, n := binary.Uvarint(d.b)
	if n <= 0 || (n >= 2 && d.b[n-1] == 0) {
		return 0, false
	}
	d.b = d.b[n:]
	return x, true
}
