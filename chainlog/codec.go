// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"math/bits"
	"strconv"

	"github.com/ethereum/go-ethereum/common"

	"github.com/hmdsefi/evmsync/chain"
)

const (
	recordVersion = 1
	headerSize    = 8 // length:uint32le crc:uint32le
	maxTopics     = 4
	flagFinal     = 0x01
	// minLogSize is the smallest encoded log: address, ntopics, an empty data length, tx hash,
	// one-byte tx index and index.
	minLogSize = 20 + 1 + 1 + 32 + 1 + 1
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Encode appends the record of e to dst and returns the extended slice. e.Offset must be >= 1
// and e.Kind valid; Encode panics otherwise. It writes only the fields of e.Kind.
//
// A record is an 8-byte header (body length and CRC-32C of the body, both little-endian uint32)
// followed by the body: version 1, the kind, the offset and the kind's payload, with integers as
// unsigned LEB128 varints. Encode does not check the body against MaxRecordSize.
func Encode(dst []byte, e Entry) []byte {
	if e.Kind < KindBlock || e.Kind > KindFinalize {
		panic("evmsync: chainlog.Encode: unknown kind " + strconv.Itoa(int(e.Kind)))
	}
	if e.Offset == 0 {
		panic("evmsync: chainlog.Encode: offset 0")
	}
	logs := e.Block.Logs
	if e.Kind == KindBlock {
		for i := range logs {
			if n := len(logs[i].Topics); n > maxTopics {
				panic("evmsync: chainlog.Encode: log " + strconv.Itoa(i) + " has " + strconv.Itoa(n) + " topics")
			}
		}
	}

	start := len(dst)
	dst = append(dst, 0, 0, 0, 0, 0, 0, 0, 0, recordVersion, byte(e.Kind))
	dst = binary.AppendUvarint(dst, uint64(e.Offset))
	if e.Kind != KindBlock {
		dst = appendRef(dst, e.To)
	} else {
		dst = appendRef(dst, e.Block.Ref)
		var flags byte
		if e.Final {
			flags = flagFinal
		}
		dst = append(dst, flags)
		dst = binary.AppendUvarint(dst, uint64(len(logs)))
		for i := range logs {
			l := &logs[i]
			dst = append(dst, l.Address[:]...)
			dst = append(dst, byte(len(l.Topics))) //nolint:gosec // at most maxTopics, checked above
			for j := range l.Topics {
				dst = append(dst, l.Topics[j][:]...)
			}
			dst = binary.AppendUvarint(dst, uint64(len(l.Data)))
			dst = append(dst, l.Data...)
			dst = append(dst, l.TxHash[:]...)
			dst = binary.AppendUvarint(dst, uint64(l.TxIndex))
			dst = binary.AppendUvarint(dst, uint64(l.Index))
		}
	}

	body := dst[start+headerSize:]
	binary.LittleEndian.PutUint32(dst[start:], uint32(len(body))) //nolint:gosec // only a body above 4 GiB overflows; Append rejects bodies above MaxRecordSize
	binary.LittleEndian.PutUint32(dst[start+4:], crc32.Checksum(body, crcTable))
	return dst
}

func appendRef(dst []byte, r chain.BlockRef) []byte {
	dst = binary.AppendUvarint(dst, r.Number)
	dst = append(dst, r.Hash[:]...)
	dst = append(dst, r.Parent[:]...)
	return binary.AppendUvarint(dst, r.Time)
}

// Decode decodes the first record in src. It returns the entry and the record's length in
// bytes. On failure it returns a zero Entry, 0 and an error wrapping one of the codec errors.
//
// Decode accepts exactly the records Encode writes. The entry does not alias src. A block's logs
// get BlockNumber and BlockHash from the block's ref, and empty Logs, Topics and Data are nil.
func Decode(src []byte) (Entry, int, error) {
	if len(src) < headerSize {
		return Entry{}, 0, fmt.Errorf("%w: have %d bytes, need %d", ErrIncomplete, len(src), headerSize)
	}
	length := binary.LittleEndian.Uint32(src)
	if length > MaxRecordSize {
		return Entry{}, 0, fmt.Errorf("%w: body of %d bytes exceeds %d", ErrRecordTooLarge, length, MaxRecordSize)
	}
	n := headerSize + int(length)
	if len(src) < n {
		return Entry{}, 0, fmt.Errorf("%w: have %d bytes, need %d", ErrIncomplete, len(src), n)
	}
	body := src[headerSize:n]
	if stored, computed := binary.LittleEndian.Uint32(src[4:]), crc32.Checksum(body, crcTable); stored != computed {
		return Entry{}, 0, fmt.Errorf("%w: stored 0x%08x, computed 0x%08x", ErrChecksum, stored, computed)
	}
	if len(body) < 3 {
		return Entry{}, 0, fmt.Errorf("%w: body of %d bytes is shorter than 3", ErrPayload, len(body))
	}
	if body[0] != recordVersion {
		return Entry{}, 0, fmt.Errorf("%w: %d", ErrVersion, body[0])
	}
	kind := Kind(body[1])
	if kind < KindBlock || kind > KindFinalize {
		return Entry{}, 0, fmt.Errorf("%w: %d", ErrKind, body[1])
	}
	e, err := decodePayload(kind, body[2:])
	if err != nil {
		return Entry{}, 0, err
	}
	return e, n, nil
}

// decodePayload decodes the body after its version and kind bytes.
func decodePayload(kind Kind, b []byte) (Entry, error) {
	d := decoder{b: b}
	off, err := d.uvarint("offset", -1)
	if err != nil {
		return Entry{}, err
	}
	if off == 0 {
		return Entry{}, fmt.Errorf("%w: offset is 0", ErrPayload)
	}
	e := Entry{Offset: Offset(off), Kind: kind}
	if kind != KindBlock {
		if e.To, err = d.ref(); err != nil {
			return Entry{}, err
		}
		return e, d.end()
	}

	if e.Block.Ref, err = d.ref(); err != nil {
		return Entry{}, err
	}
	flags, err := d.byte("flags", -1)
	if err != nil {
		return Entry{}, err
	}
	if flags&^flagFinal != 0 {
		return Entry{}, fmt.Errorf("%w: flags 0x%02x has unknown bits", ErrPayload, flags)
	}
	e.Final = flags == flagFinal
	nlogs, err := d.uvarint("nlogs", -1)
	if err != nil {
		return Entry{}, err
	}
	// Bound nlogs by the remaining bytes before anything is allocated for the logs.
	if nlogs > uint64(len(d.b)/minLogSize) {
		return Entry{}, fmt.Errorf("%w: %d logs cannot fit in %d bytes", ErrPayload, nlogs, len(d.b))
	}
	if nlogs == 0 {
		return e, d.end()
	}

	// Two passes over the logs: the first validates them and counts their topics and data bytes,
	// the second fills one []Log, one []common.Hash and one []byte.
	logStart := d
	ntopics, ndata, err := d.logs(int(nlogs), nil, nil, nil) //nolint:gosec // nlogs <= MaxRecordSize/minLogSize, checked above
	if err != nil {
		return Entry{}, err
	}
	if err := d.end(); err != nil {
		return Entry{}, err
	}
	logs := make([]chain.Log, nlogs)
	var topics []common.Hash
	if ntopics > 0 {
		topics = make([]common.Hash, ntopics)
	}
	var data []byte
	if ndata > 0 {
		data = make([]byte, ndata)
	}
	d = logStart
	if _, _, err := d.logs(len(logs), logs, topics, data); err != nil {
		return Entry{}, err // unreachable: the first pass validated these bytes
	}
	for i := range logs {
		logs[i].BlockNumber = e.Block.Ref.Number
		logs[i].BlockHash = e.Block.Ref.Hash
	}
	e.Block.Logs = logs
	return e, nil
}

// decoder reads the fields of a record body in order. Its errors wrap ErrPayload.
type decoder struct {
	b []byte // the bytes not read yet
}

// logs reads n logs. With logs nil it only validates them and returns their total number of
// topics and data bytes. Otherwise it fills logs[i], sub-slicing its Topics from topics and its
// Data from data, which must have the totals of a validating pass over the same bytes.
func (d *decoder) logs(n int, logs []chain.Log, topics []common.Hash, data []byte) (ntopics, ndata int, err error) {
	for i := range n {
		addr, err := d.take(common.AddressLength, "address", i)
		if err != nil {
			return 0, 0, err
		}
		nt, err := d.byte("ntopics", i)
		if err != nil {
			return 0, 0, err
		}
		if nt > maxTopics {
			return 0, 0, fmt.Errorf("%w: log %d: %d topics, at most %d", ErrPayload, i, nt, maxTopics)
		}
		tb, err := d.take(common.HashLength*int(nt), "topics", i)
		if err != nil {
			return 0, 0, err
		}
		dl, err := d.uvarint("data", i)
		if err != nil {
			return 0, 0, err
		}
		if dl > uint64(len(d.b)) {
			return 0, 0, errTruncated("data", i)
		}
		db, _ := d.take(int(dl), "data", i) //nolint:gosec // dl <= len(d.b), checked above
		txHash, err := d.take(common.HashLength, "txhash", i)
		if err != nil {
			return 0, 0, err
		}
		txIndex, err := d.uvarint("txindex", i)
		if err != nil {
			return 0, 0, err
		}
		if txIndex > math.MaxUint32 {
			return 0, 0, fmt.Errorf("%w: log %d: tx index %d exceeds %d", ErrPayload, i, txIndex, uint64(math.MaxUint32))
		}
		index, err := d.uvarint("index", i)
		if err != nil {
			return 0, 0, err
		}
		if index > math.MaxUint32 {
			return 0, 0, fmt.Errorf("%w: log %d: index %d exceeds %d", ErrPayload, i, index, uint64(math.MaxUint32))
		}

		if logs != nil {
			l := &logs[i]
			copy(l.Address[:], addr)
			if nt > 0 {
				l.Topics = topics[ntopics : ntopics+int(nt) : ntopics+int(nt)]
				for j := range l.Topics {
					copy(l.Topics[j][:], tb[j*common.HashLength:])
				}
			}
			if len(db) > 0 {
				l.Data = data[ndata : ndata+len(db) : ndata+len(db)]
				copy(l.Data, db)
			}
			copy(l.TxHash[:], txHash)
			l.TxIndex = uint32(txIndex)
			l.Index = uint32(index)
		}
		ntopics += int(nt)
		ndata += len(db)
	}
	return ntopics, ndata, nil
}

func (d *decoder) ref() (chain.BlockRef, error) {
	var r chain.BlockRef
	var err error
	if r.Number, err = d.uvarint("ref.number", -1); err != nil {
		return chain.BlockRef{}, err
	}
	hash, err := d.take(common.HashLength, "ref.hash", -1)
	if err != nil {
		return chain.BlockRef{}, err
	}
	parent, err := d.take(common.HashLength, "ref.parent", -1)
	if err != nil {
		return chain.BlockRef{}, err
	}
	if r.Time, err = d.uvarint("ref.time", -1); err != nil {
		return chain.BlockRef{}, err
	}
	copy(r.Hash[:], hash)
	copy(r.Parent[:], parent)
	return r, nil
}

// take returns the next n bytes. field and log name the field in the error (log -1: not a log
// field).
func (d *decoder) take(n int, field string, log int) ([]byte, error) {
	if n > len(d.b) {
		return nil, errTruncated(field, log)
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p, nil
}

func (d *decoder) byte(field string, log int) (byte, error) {
	p, err := d.take(1, field, log)
	if err != nil {
		return 0, err
	}
	return p[0], nil
}

// uvarint reads a minimal unsigned LEB128 varint.
func (d *decoder) uvarint(field string, log int) (uint64, error) {
	x, n := binary.Uvarint(d.b)
	switch {
	case n == 0:
		return 0, errTruncated(field, log)
	case n < 0:
		return 0, fmt.Errorf("%w: %s: varint overflows uint64", ErrPayload, fieldName(field, log))
	case n >= 2 && d.b[n-1] == 0:
		// binary.Uvarint accepts a padded encoding such as 80 00; Encode never writes one.
		return 0, fmt.Errorf("%w: %s: non-minimal varint", ErrPayload, fieldName(field, log))
	}
	d.b = d.b[n:]
	return x, nil
}

// end fails when bytes remain after the payload.
func (d *decoder) end() error {
	if len(d.b) > 0 {
		return fmt.Errorf("%w: %d bytes after the payload", ErrTrailing, len(d.b))
	}
	return nil
}

func errTruncated(field string, log int) error {
	return fmt.Errorf("%w: %s: truncated", ErrPayload, fieldName(field, log))
}

// fieldName returns field, or "log[<log>].<field>" for a field of log number log >= 0.
func fieldName(field string, log int) string {
	if log < 0 {
		return field
	}
	return "log[" + strconv.Itoa(log) + "]." + field
}

// recordBodySize returns the length of e's record body as Encode writes it, counting the offset
// as 10 bytes (the longest varint), so the result does not depend on the offset Append assigns.
// It allocates nothing and encodes nothing.
func recordBodySize(e Entry) int {
	const head = 2 + binary.MaxVarintLen64 // version, kind, offset
	if e.Kind != KindBlock {
		return head + refSize(e.To)
	}
	logs := e.Block.Logs
	n := head + refSize(e.Block.Ref) + 1 + uvarintLen(uint64(len(logs)))
	for i := range logs {
		l := &logs[i]
		n += common.AddressLength + 1 + common.HashLength*len(l.Topics) +
			uvarintLen(uint64(len(l.Data))) + len(l.Data) + common.HashLength +
			uvarintLen(uint64(l.TxIndex)) + uvarintLen(uint64(l.Index))
	}
	return n
}

func refSize(r chain.BlockRef) int {
	return uvarintLen(r.Number) + 2*common.HashLength + uvarintLen(r.Time)
}

// uvarintLen returns the number of bytes binary.AppendUvarint writes for x.
func uvarintLen(x uint64) int { return (bits.Len64(x|1) + 6) / 7 }
