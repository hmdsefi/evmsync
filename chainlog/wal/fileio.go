// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	fileMode = 0o600
	dirMode  = 0o750
)

// writeFileAtomic replaces dir/name with data: it writes name.tmp, fsyncs it when sync is set,
// renames it to name, and then fsyncs dir when sync is set. A crash leaves either the old file or
// the new one, and possibly name.tmp, which Open deletes.
func writeFileAtomic(dir, name string, data []byte, sync bool) (err error) {
	tmp := filepath.Join(dir, name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("wal: open %s: %w", tmp, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("wal: write %s: %w", tmp, cerr)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("wal: write %s: %w", tmp, err)
	}
	if sync {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("wal: sync %s: %w", tmp, err)
		}
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("wal: rename %s: %w", path, err)
	}
	if sync {
		return syncDir(dir)
	}
	return nil
}

// syncDir fsyncs the directory dir, so the names created, renamed or removed in it are durable.
func syncDir(dir string) (err error) {
	d, err := os.Open(dir) //nolint:gosec // dir is the log's own directory
	if err != nil {
		return fmt.Errorf("wal: open %s: %w", dir, err)
	}
	defer func() {
		if cerr := d.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("wal: sync %s: %w", dir, cerr)
		}
	}()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("wal: sync %s: %w", dir, err)
	}
	return nil
}
