// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package archtest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// header is the first bytes of every .go file: the two lines of architecture §15.6 and a blank line.
const header = "// Copyright 2026 Hamed Yousefi\n// SPDX-License-Identifier: Apache-2.0\n\n"

// checkLicense reports every .go file below root, nested modules included, that does not start
// with header.
func checkLicense(root string) ([]finding, error) {
	var fs []finding
	err := walk(root, false, func(dir, rel string, entries []os.DirEntry) error {
		for _, e := range entries {
			if !isGoFile(e) {
				continue
			}
			ok, err := hasHeader(filepath.Join(dir, e.Name()))
			if err != nil {
				return err
			}
			if !ok {
				fs = append(fs, finding{
					file: relFile(rel, e.Name()), line: 1, col: 1, rule: "LH1",
					msg: "missing the license header of architecture §15.6 (two comment lines, then a blank line)",
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortFindings(fs)
	return fs, nil
}

// hasHeader reports whether the file starts with header.
func hasHeader(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("archtest: %w", err)
	}
	defer f.Close()
	buf := make([]byte, len(header))
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("archtest: %w", err)
	}
	return bytes.Equal(buf[:n], []byte(header)), nil
}

func TestLicenseHeaders(t *testing.T) {
	fs, err := checkLicense(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	report(t, "license headers", fs)
}

func TestLicenseFixtures(t *testing.T) {
	root := copyFixture(t, "license")
	// A file with \r\n line endings is never gofmt-clean, so it is written here, not committed.
	crlf := "// Copyright 2026 Hamed Yousefi\r\n// SPDX-License-Identifier: Apache-2.0\r\n\r\npackage license\r\n"
	if err := os.WriteFile(filepath.Join(root, "crlf.go"), []byte(crlf), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := checkLicense(root)
	if err != nil {
		t.Fatal(err)
	}
	const msg = ":1:1: LH1: missing the license header of architecture §15.6 (two comment lines, then a blank line)"
	equalLines(t, strs(fs), []string{
		"crlf.go" + msg,
		"line2.go" + msg,
		"mod/x.go" + msg,
		"noblank.go" + msg,
		"year.go" + msg,
	})
}
