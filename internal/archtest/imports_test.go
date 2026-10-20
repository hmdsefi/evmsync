// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package archtest

import (
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// checkContexts are the build contexts a file must match one of to be checked: cgo off, and
// linux/amd64, darwin/arm64 and windows/amd64.
func checkContexts() []build.Context {
	var cs []build.Context
	for _, p := range [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		c := build.Default
		c.CgoEnabled = false
		c.GOOS, c.GOARCH = p[0], p[1]
		cs = append(cs, c)
	}
	return cs
}

// inPlay reports whether at least one context matches the file.
func inPlay(cs []build.Context, dir, name string) (bool, error) {
	for _, c := range cs {
		ok, err := c.MatchFile(dir, name)
		if err != nil {
			return false, fmt.Errorf("archtest: %w", err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// importPath returns the import path of the root-module package at rel.
func importPath(rel string) string {
	if rel == "" {
		return modulePath
	}
	return modulePath + "/" + rel
}

// allowed reports whether a package with rule r may import p.
func allowed(p string, r rule) bool {
	switch {
	case p == "C":
		return false
	case p == modulePath || strings.HasPrefix(p, modulePath+"/"):
		rel := strings.TrimPrefix(strings.TrimPrefix(p, modulePath), "/")
		return slices.Contains(r.module, rel)
	case !strings.Contains(strings.Split(p, "/")[0], "."):
		switch {
		case p == "testing" || strings.HasPrefix(p, "testing/"):
			return r.testing && p == "testing"
		case r.noNet && (p == "net" || strings.HasPrefix(p, "net/") || p == "os/exec" ||
			p == "math/rand" || p == "math/rand/v2"):
			return false
		case r.noTime && p == "time":
			return false
		}
		return true
	default:
		return slices.Contains(r.external, p)
	}
}

// checkImports checks the imports of every package directory below root against rs, and the set
// of package directories against the inventory inv.
func checkImports(root string, inv []string, rs map[string]rule) ([]finding, error) {
	var fs []finding
	fset := token.NewFileSet()
	cs := checkContexts()
	pkgs := map[string]bool{}
	err := walk(root, true, func(dir, rel string, entries []os.DirEntry) error {
		if !slices.ContainsFunc(entries, isGoFile) {
			return nil
		}
		pkgs[rel] = true
		r, ok := rs[rel]
		if !ok {
			return nil
		}
		for _, e := range entries {
			name := e.Name()
			if !isGoFile(e) || strings.HasSuffix(name, "_test.go") {
				continue
			}
			ok, err := inPlay(cs, dir, name)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
			if err != nil {
				return fmt.Errorf("archtest: %w", err)
			}
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return fmt.Errorf("archtest: %s: %w", fset.Position(imp.Path.Pos()), err)
				}
				if allowed(p, r) {
					continue
				}
				pos := fset.Position(imp.Path.Pos())
				fs = append(fs, finding{
					file: relFile(rel, name), line: pos.Line, col: pos.Column, rule: "IB1",
					msg: fmt.Sprintf("import %q is not allowed in package %s", p, importPath(rel)),
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	ib0 := func(rel, msg string) {
		fs = append(fs, finding{file: rel, rule: "IB0", msg: fmt.Sprintf("package %s %s", rel, msg)})
	}
	seen := map[string]int{}
	for _, rel := range inv {
		seen[rel]++
	}
	for _, rel := range slices.Sorted(maps.Keys(pkgs)) {
		if seen[rel] == 0 {
			ib0(rel, "exists but is not in the inventory")
		}
		if _, ok := rs[rel]; !ok {
			ib0(rel, "has no import rule; add it to architecture §5.1 and internal/archtest")
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(seen)) {
		if !pkgs[rel] {
			ib0(rel, "is in the inventory but does not exist")
		}
		if seen[rel] > 1 {
			ib0(rel, "is listed twice in the inventory")
		}
	}
	sortFindings(fs)
	return fs, nil
}

func TestImportBoundaries(t *testing.T) {
	fs, err := checkImports(testRoot(t), inventory, rules)
	if err != nil {
		t.Fatal(err)
	}
	report(t, "import boundaries", fs)
}

func TestImportFixtures(t *testing.T) {
	root := copyFixture(t, "imports")
	inv := []string{"chain", "chainlog", "chainlog/logtest", "chainlog/wal", "consumer", "ingest", "rpc", "tracker"}
	fs, err := checkImports(root, inv, rules)
	if err != nil {
		t.Fatal(err)
	}
	const pkg = "is not allowed in package " + modulePath
	equalLines(t, strs(fs), []string{
		`chain/bad.go:5:2: IB1: import "net/http" ` + pkg + `/chain`,
		`chain/cgo.go:3:8: IB1: import "C" ` + pkg + `/chain`,
		`chain/win_windows.go:3:8: IB1: import "os/exec" ` + pkg + `/chain`,
		`chainlog/l.go:3:8: IB1: import "github.com/hmdsefi/evmsync/tracker" ` + pkg + `/chainlog`,
		`chainlog/wal/w.go:3:8: IB1: import "github.com/jackc/pgx/v5" ` + pkg + `/chainlog/wal`,
		`consumer/c.go:3:8: IB1: import "math/rand/v2" ` + pkg + `/consumer`,
		`ingest:0:0: IB0: package ingest is in the inventory but does not exist`,
		`newpkg:0:0: IB0: package newpkg exists but is not in the inventory`,
		`newpkg:0:0: IB0: package newpkg has no import rule; add it to architecture §5.1 and internal/archtest`,
		`rpc/r.go:3:8: IB1: import "testing" ` + pkg + `/rpc`,
		`tracker/t.go:3:8: IB1: import "time" ` + pkg + `/tracker`,
	})
}

func TestInventoryDuplicate(t *testing.T) {
	root := copyFixture(t, "imports")
	inv := []string{"chain", "chain", "chainlog", "chainlog/logtest", "chainlog/wal", "consumer", "rpc", "tracker", "newpkg"}
	fs, err := checkImports(root, inv, rules)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strs(fs), "chain:0:0: IB0: package chain is listed twice in the inventory") {
		t.Errorf("no duplicate finding in:\n%s", strings.Join(strs(fs), "\n"))
	}
}
