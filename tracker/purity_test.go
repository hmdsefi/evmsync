// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedImports are the only packages the tracker's non-test files may import.
var allowedImports = map[string]bool{
	"context":                                true,
	"errors":                                 true,
	"fmt":                                    true,
	"github.com/ethereum/go-ethereum/common": true,
	"github.com/hmdsefi/evmsync/chain":       true,
	"github.com/hmdsefi/evmsync/chainlog":    true,
}

// TestPurity checks that the package's non-test files import only allowedImports and contain no
// go statement, no select statement and no range over a map.
func TestPurity(t *testing.T) {
	fset := token.NewFileSet()
	files := parseNonTest(t, fset, ".")
	if len(files) == 0 {
		t.Fatal("no non-test files found")
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	if _, err := conf.Check("github.com/hmdsefi/evmsync/tracker", fset, files, info); err != nil {
		t.Fatalf("type-check: %v", err)
	}
	for _, f := range files {
		for _, msg := range purityFindings(fset, f, info) {
			t.Error(msg)
		}
	}
}

// TestPurityFindings checks that purityFindings reports each forbidden construct.
func TestPurityFindings(t *testing.T) {
	const src = `package p

import (
	"sync"
	"time"
)

var m = map[int]int{}

type named map[string]bool

var n named

func f(c chan int) {
	go func() {}()
	select {
	case <-c:
	}
	for range m {
	}
	for range []int{} {
	}
	for range n {
	}
	_ = sync.Mutex{}
	_ = time.Second
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	if _, err := conf.Check("p", fset, []*ast.File{f}, info); err != nil {
		t.Fatalf("type-check: %v", err)
	}
	want := []string{
		`p.go:4:2: import "sync" is not allowed`,
		`p.go:5:2: import "time" is not allowed`,
		"p.go:15:2: go statement",
		"p.go:16:2: select statement",
		"p.go:19:2: range over a map",
		"p.go:23:2: range over a map",
	}
	if got := purityFindings(fset, f, info); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// parseNonTest parses every non-test .go file in dir.
func parseNonTest(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// purityFindings returns one message per forbidden import, go statement, select statement and
// range over a map in f, in source order.
func purityFindings(fset *token.FileSet, f *ast.File, info *types.Info) []string {
	var out []string
	report := func(n ast.Node, msg string) {
		out = append(out, fset.Position(n.Pos()).String()+": "+msg)
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !allowedImports[path] {
			report(imp, "import "+imp.Path.Value+" is not allowed")
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			report(n, "go statement")
		case *ast.SelectStmt:
			report(n, "select statement")
		case *ast.RangeStmt:
			if tv, ok := info.Types[n.X]; ok {
				if _, isMap := tv.Type.Underlying().(*types.Map); isMap {
					report(n, "range over a map")
				}
			}
		}
		return true
	})
	return out
}
