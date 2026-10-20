// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"encoding/binary"
	"fmt"

	"github.com/hmdsefi/evmsync/chainlog"
)

const (
	checkpointName    = "checkpoint"
	checkpointMagic   = "EVMSCKPT"
	checkpointVersion = 1
	maxCheckpointSize = 1 << 30 // the largest checkpoint body, in bytes
)

// checkpoint is the validator state after offset x, so Open replays only the entries after x.
type checkpoint struct {
	x, l  chainlog.Offset // state after offset x; last finalize offset l <= x
	state chainlog.State
}

// encodeCheckpoint returns the checkpoint file for c: the same 8-byte header as meta, then a body
// of the magic, version 1, x and l as varints, and the state's binary form.
func encodeCheckpoint(c checkpoint) []byte {
	state, _ := c.state.MarshalBinary() // never fails
	b := make([]byte, fileHeader, fileHeader+len(checkpointMagic)+1+2*binary.MaxVarintLen64+len(state))
	b = append(b, checkpointMagic...)
	b = append(b, checkpointVersion)
	b = binary.AppendUvarint(b, uint64(c.x))
	b = binary.AppendUvarint(b, uint64(c.l))
	b = append(b, state...)
	return sealFile(b)
}

// decodeCheckpoint decodes a checkpoint file. The state gets window cap windowCap. Its errors wrap
// ErrCorrupt and read "wal: corrupt log: checkpoint: <detail>"; a bad state also wraps
// chainlog.ErrBadState.
func decodeCheckpoint(b []byte, windowCap int) (checkpoint, error) {
	corrupt := func(detail string) (checkpoint, error) {
		return checkpoint{}, fmt.Errorf("%w: checkpoint: %s", ErrCorrupt, detail)
	}
	body, detail := openFile(b, maxCheckpointSize)
	if detail != "" {
		return corrupt(detail)
	}
	d := reader{b: body}
	magic, ok := d.take(len(checkpointMagic))
	if !ok {
		return corrupt("truncated")
	}
	if string(magic) != checkpointMagic {
		return corrupt("bad magic")
	}
	version, ok := d.take(1)
	if !ok {
		return corrupt("truncated")
	}
	if version[0] != checkpointVersion {
		return corrupt(fmt.Sprintf("version %d", version[0]))
	}
	x, okX := d.uvarint()
	l, okL := d.uvarint()
	if !okX || !okL || l > x {
		return corrupt("bad header")
	}
	c := checkpoint{x: chainlog.Offset(x), l: chainlog.Offset(l), state: chainlog.NewState(windowCap)}
	if err := c.state.UnmarshalBinary(d.b); err != nil {
		return checkpoint{}, fmt.Errorf("%w: checkpoint: %w", ErrCorrupt, err)
	}
	return c, nil
}
