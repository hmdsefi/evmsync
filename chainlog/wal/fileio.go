// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	fileMode   = 0o600
	dirMode    = 0o750
	fileHeader = 8 // the header of meta and checkpoint: length:uint32le crc:uint32le
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

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

// ioErr returns err as "wal: <op> <path>: <err>". An error of the os package already names its
// operation and path, so only its cause is kept; errors.Is still matches it.
func ioErr(op, path string, err error) error {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		err = pe.Err
	case errors.As(err, &le):
		err = le.Err
	}
	return fmt.Errorf("wal: %s %s: %w", op, path, err)
}

// writeFileAtomic replaces dir/name with data: it writes and closes name.tmp, fsyncs it when sync
// is set, renames it to name, and then fsyncs dir when sync is set. With sync set, a crash leaves
// either the old file or the new one, and possibly name.tmp, which Open deletes. On an error it
// removes name.tmp.
func writeFileAtomic(dir, name string, data []byte, sync bool) error {
	tmp := filepath.Join(dir, name+".tmp")
	if err := writeFile(tmp, data, sync); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return ioErr("rename", path, err)
	}
	if sync {
		return syncDir(dir)
	}
	return nil
}

// writeFile creates path with data, fsyncs it when sync is set, and closes it.
func writeFile(path string, data []byte, sync bool) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode) //nolint:gosec // a file name the WAL owns
	if err != nil {
		return ioErr("open", path, err)
	}
	if _, err := f.Write(data); err != nil {
		return errors.Join(ioErr("write", path, err), f.Close())
	}
	if sync {
		if err := f.Sync(); err != nil {
			return errors.Join(ioErr("sync", path, err), f.Close())
		}
	}
	if err := f.Close(); err != nil {
		return ioErr("write", path, err)
	}
	return nil
}

// syncDir fsyncs the directory dir, so the names created, renamed or removed in it are durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the log's own directory
	if err != nil {
		return ioErr("open", dir, err)
	}
	if err := d.Sync(); err != nil {
		return errors.Join(ioErr("sync", dir, err), d.Close())
	}
	if err := d.Close(); err != nil {
		return ioErr("sync", dir, err)
	}
	return nil
}
