// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
)

const (
	metaName     = "meta"
	metaMagic    = "EVMSMETA"
	metaVersion  = 1
	maxMetaSize  = 1 << 20 // the largest meta body, in bytes
	addressLen   = 20
	hashLen      = 32
	maxFilterLen = chain.MaxFilterValues
)

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
