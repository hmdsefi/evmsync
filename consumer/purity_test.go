// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// impure returns one finding per go statement in f and per import of time, math/rand,
// math/rand/v2, net or a net/ package, os/exec or testing.
func impure(fset *token.FileSet, f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		switch {
		case path == "time", path == "math/rand", path == "math/rand/v2", path == "os/exec", path == "testing",
			path == "net", strings.HasPrefix(path, "net/"):
			out = append(out, fset.Position(imp.Pos()).String()+": imports "+path)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if g, ok := n.(*ast.GoStmt); ok {
			out = append(out, fset.Position(g.Pos()).String()+": go statement")
		}
		return true
	})
	return out
}

func TestPurity(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range impure(fset, f) {
			t.Error(finding)
		}
	}
	if !slices.Contains(names, "consumer.go") {
		t.Fatalf("checked %v, want consumer.go among them", names)
	}
}

func TestImpureFindings(t *testing.T) {
	const src = `package p

import (
	"fmt"
	"math/rand"
	_ "math/rand/v2"
	_ "net"
	"net/http"
	_ "netpkg"
	_ "os/exec"
	_ "testing"
	"time"
)

func f() { go time.Sleep(0); _ = rand.Int; _ = http.Get }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"p.go:5:2: imports math/rand",
		"p.go:6:2: imports math/rand/v2",
		"p.go:7:2: imports net",
		"p.go:8:2: imports net/http",
		"p.go:10:2: imports os/exec",
		"p.go:11:2: imports testing",
		"p.go:12:2: imports time",
		"p.go:15:12: go statement",
	}
	if got := impure(fset, f); !slices.Equal(got, want) {
		t.Errorf("impure = %q, want %q", got, want)
	}
}
