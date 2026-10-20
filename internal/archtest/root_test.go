// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package archtest

import (
	"cmp"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// modulePath is the root module's path.
const modulePath = "github.com/hmdsefi/evmsync"

// finding is one rule violation.
type finding struct {
	file      string // relative to the checked root, "/" separators
	line, col int
	rule, msg string
}

func (f finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", f.file, f.line, f.col, f.rule, f.msg)
}

// sortFindings sorts by file, line, column, rule and message.
func sortFindings(fs []finding) {
	slices.SortFunc(fs, func(a, b finding) int {
		return cmp.Or(
			cmp.Compare(a.file, b.file),
			cmp.Compare(a.line, b.line),
			cmp.Compare(a.col, b.col),
			cmp.Compare(a.rule, b.rule),
			cmp.Compare(a.msg, b.msg),
		)
	})
}

// strs returns the findings as strings, in order.
func strs(fs []finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.String()
	}
	return out
}

// report sorts the findings and fails the test once with all of them.
func report(t *testing.T, topic string, fs []finding) {
	t.Helper()
	if len(fs) == 0 {
		return
	}
	sortFindings(fs)
	t.Errorf("%s: %d finding(s):\n%s", topic, len(fs), strings.Join(strs(fs), "\n"))
}

// moduleRoot walks up from dir to the first directory whose go.mod declares modulePath.
func moduleRoot(dir string) (string, error) {
	for d := dir; ; {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil && declaresModule(string(data)) {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("archtest: no go.mod for %s above %s", modulePath, dir)
		}
		d = parent
	}
}

// declaresModule reports whether a go.mod's text has the line "module github.com/hmdsefi/evmsync".
func declaresModule(gomod string) bool {
	for line := range strings.Lines(gomod) {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "module" && strings.Trim(f[1], `"`) == modulePath {
			return true
		}
	}
	return false
}

// testRoot returns the module root above the test's working directory.
func testRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := moduleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// walk calls fn for root and every directory below it, in lexical order, with the directory's
// relative path ("" for root, "/" separators) and its entries. It skips directories named testdata
// or vendor, directories whose name starts with "." or "_", and, when skipNested is set, every
// directory below root that holds a go.mod.
func walk(root string, skipNested bool, fn func(dir, rel string, entries []os.DirEntry) error) error {
	var visit func(dir, rel string) error
	visit = func(dir, rel string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("archtest: %w", err)
		}
		if rel != "" && skipNested && slices.ContainsFunc(entries, isGoMod) {
			return nil
		}
		if err := fn(dir, rel, entries); err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || name == "testdata" || name == "vendor" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			if err := visit(filepath.Join(dir, name), relFile(rel, name)); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root, "")
}

// relFile joins a relative directory and a file name with "/".
func relFile(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func isGoMod(e os.DirEntry) bool {
	return e.Name() == "go.mod" && !e.IsDir()
}

// isGoFile reports whether e is a file named *.go.
func isGoFile(e os.DirEntry) bool {
	return !e.IsDir() && strings.HasSuffix(e.Name(), ".go")
}

// copyFixture copies testdata/<name> to a temporary directory, renames every go.mod.tmpl to
// go.mod, and returns the copy's path.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("testdata", name))); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	err = fs.WalkDir(r.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "go.mod.tmpl" {
			return err
		}
		return r.Rename(name, path.Join(path.Dir(name), "go.mod"))
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// equalLines fails the test when got and want differ, showing both.
func equalLines(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("findings differ\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
