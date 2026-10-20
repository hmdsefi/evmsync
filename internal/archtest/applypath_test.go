// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package archtest

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// applyPath lists the apply-path packages, as relative paths; the ones that exist are checked.
var applyPath = []string{"chain", "chainlog", "tracker", "consumer", "memstore"}

// wallClock lists the functions of package time that read or wait on the wall clock.
var wallClock = []string{"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker"}

// sortedFuncs are the functions of package slices whose first argument may be a maps iterator.
var sortedFuncs = []string{"Sorted", "SortedFunc", "SortedStableFunc"}

// maporderRE matches the text after "//" of a maporder directive.
var maporderRE = regexp.MustCompile(`^evmsync:maporder(?:[ \t]+(.*\S))?[ \t]*$`)

// pkgDir is a package directory and the import path to type-check it as.
type pkgDir struct{ dir, path string }

// checkApplyPath type-checks each package and reports wall-clock reads (AP1), global randomness
// (AP2), go statements (AP3), map ranges (AP4) and bad maporder directives (AP0). Finding files are
// relative to root.
func checkApplyPath(root string, pkgs []pkgDir) ([]finding, error) {
	fset := token.NewFileSet()
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	ctx := build.Default
	ctx.CgoEnabled = false
	var fs []finding
	for _, p := range pkgs {
		pf, err := checkPackage(fset, &conf, &ctx, root, p)
		if err != nil {
			return nil, err
		}
		fs = append(fs, pf...)
	}
	sortFindings(fs)
	return fs, nil
}

// source is one parsed file with its bytes and its path relative to the checked root.
type source struct {
	file *ast.File
	src  []byte
	rel  string
}

// checker collects the findings of one package.
type checker struct {
	fset  *token.FileSet
	info  *types.Info
	pkg   *types.Package
	rel   map[string]string // absolute file name to relative path
	found []finding
}

func checkPackage(fset *token.FileSet, conf *types.Config, ctx *build.Context, root string, p pkgDir) ([]finding, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, fmt.Errorf("archtest: %w", err)
	}
	var srcs []source
	for _, e := range entries {
		name := e.Name()
		if !isGoFile(e) || strings.HasSuffix(name, "_test.go") {
			continue
		}
		ok, err := ctx.MatchFile(p.dir, name)
		if err != nil {
			return nil, fmt.Errorf("archtest: %w", err)
		}
		if !ok {
			continue
		}
		path := filepath.Join(p.dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("archtest: %w", err)
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("archtest: %w", err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("archtest: %w", err)
		}
		srcs = append(srcs, source{file: f, src: src, rel: filepath.ToSlash(rel)})
	}
	if len(srcs) == 0 {
		return nil, nil
	}
	files := make([]*ast.File, len(srcs))
	c := &checker{
		fset: fset,
		info: &types.Info{Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}},
		rel:  map[string]string{},
	}
	for i, s := range srcs {
		files[i] = s.file
		c.rel[fset.Position(s.file.Pos()).Filename] = s.rel
	}
	c.pkg, err = conf.Check(p.path, fset, files, c.info)
	if err != nil {
		return nil, fmt.Errorf("archtest: type-check %s: %w", p.path, err)
	}
	c.uses(sortedArgs(c.info, files))
	for _, f := range files {
		c.goStmts(f)
		c.mapRanges(f)
	}
	var out []finding
	for _, s := range srcs {
		out = append(out, applyDirectives(c.fileFindings(s.rel), directives(fset, s))...)
	}
	return out, nil
}

// add records a finding at pos.
func (c *checker) add(pos token.Pos, rule, msg string) {
	p := c.fset.Position(pos)
	c.found = append(c.found, finding{file: c.rel[p.Filename], line: p.Line, col: p.Column, rule: rule, msg: msg})
}

// fileFindings returns the findings in the file rel.
func (c *checker) fileFindings(rel string) []finding {
	var out []finding
	for _, f := range c.found {
		if f.file == rel {
			out = append(out, f)
		}
	}
	return out
}

// uses reports uses of wall-clock functions (AP1), of package-level functions of math/rand and
// math/rand/v2 (AP2), and of maps.All, maps.Keys and maps.Values (AP4) unless the identifier is
// in sorted.
func (c *checker) uses(sorted map[*ast.Ident]bool) {
	for id, obj := range c.info.Uses {
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Signature().Recv() != nil {
			continue
		}
		name := fn.Name()
		switch path := fn.Pkg().Path(); {
		case path == "time" && slices.Contains(wallClock, name):
			c.add(id.Pos(), "AP1", fmt.Sprintf("wall-clock time: time.%s is forbidden in apply-path code; read time from the block", name))
		case path == "math/rand" || path == "math/rand/v2":
			c.add(id.Pos(), "AP2", fmt.Sprintf("global randomness: %s.%s is forbidden in apply-path code", path, name))
		case path == "maps" && (name == "All" || name == "Keys" || name == "Values") && !sorted[id]:
			c.add(id.Pos(), "AP4", fmt.Sprintf("maps.%s yields in random order; wrap it in slices.Sorted or annotate //evmsync:maporder <reason>", name))
		}
	}
}

// sortedArgs returns the callee identifiers of calls that are the first argument of a call to
// slices.Sorted, SortedFunc or SortedStableFunc.
func sortedArgs(info *types.Info, files []*ast.File) map[*ast.Ident]bool {
	out := map[*ast.Ident]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			fn, ok := info.Uses[callee(call)].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "slices" || !slices.Contains(sortedFuncs, fn.Name()) {
				return true
			}
			if arg, ok := ast.Unparen(call.Args[0]).(*ast.CallExpr); ok {
				if id := callee(arg); id != nil {
					out[id] = true
				}
			}
			return true
		})
	}
	return out
}

// callee returns the identifier a call's function expression names: f, pkg.f, f[T] or pkg.f[T].
func callee(call *ast.CallExpr) *ast.Ident {
	fun := ast.Unparen(call.Fun)
	switch x := fun.(type) {
	case *ast.IndexExpr:
		fun = x.X
	case *ast.IndexListExpr:
		fun = x.X
	}
	switch x := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return x
	case *ast.SelectorExpr:
		return x.Sel
	}
	return nil
}

// goStmts reports every go statement (AP3) except those inside the body of the function Run
// without receiver in package consumer.
func (c *checker) goStmts(f *ast.File) {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == "Run" && c.pkg.Name() == "consumer" {
			continue
		}
		ast.Inspect(d, func(n ast.Node) bool {
			if g, ok := n.(*ast.GoStmt); ok {
				c.add(g.Go, "AP3", "go statement is forbidden in apply-path code; only consumer.Run may start goroutines")
			}
			return true
		})
	}
}

// mapRanges reports every range over a map or over a type parameter whose constraint allows a
// map (AP4).
func (c *checker) mapRanges(f *ast.File) {
	qual := func(p *types.Package) string {
		if p == c.pkg {
			return ""
		}
		return p.Name()
	}
	ast.Inspect(f, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if t := c.info.TypeOf(rs.X); t != nil && allowsMap(t) {
			c.add(rs.For, "AP4", fmt.Sprintf("range over map %s has random order; range over sorted keys or annotate //evmsync:maporder <reason>", types.TypeString(t, qual)))
		}
		return true
	})
}

// allowsMap reports whether t is a map, or a type parameter or interface whose embedded types or
// union terms allow a map, at any depth.
func allowsMap(t types.Type) bool {
	if tp, ok := t.(*types.TypeParam); ok {
		return allowsMap(tp.Constraint())
	}
	switch u := t.Underlying().(type) {
	case *types.Map:
		return true
	case *types.Interface:
		for i := range u.NumEmbeddeds() {
			e := u.EmbeddedType(i)
			if un, ok := e.(*types.Union); ok {
				for j := range un.Len() {
					if allowsMap(un.Term(j).Type()) {
						return true
					}
				}
			} else if allowsMap(e) {
				return true
			}
		}
	}
	return false
}

// directive is one //evmsync:maporder comment.
type directive struct {
	pos    token.Position
	file   string // relative path
	line   int    // the line it applies to
	reason string
}

// directives returns the maporder directives of a file. A directive applies to its own line when
// code precedes it on that line, otherwise to the next line.
func directives(fset *token.FileSet, s source) []directive {
	var out []directive
	for _, cg := range s.file.Comments {
		for _, cm := range cg.List {
			if !strings.HasPrefix(cm.Text, "//") {
				continue
			}
			m := maporderRE.FindStringSubmatch(cm.Text[2:])
			if m == nil {
				continue
			}
			pos := fset.Position(cm.Slash)
			before := s.src[pos.Offset-(pos.Column-1) : pos.Offset]
			line := pos.Line + 1
			if strings.Trim(string(before), " \t") != "" {
				line = pos.Line
			}
			out = append(out, directive{pos: pos, file: s.rel, line: line, reason: m[1]})
		}
	}
	return out
}

// applyDirectives drops the AP4 findings on lines a directive with a reason applies to, and adds
// an AP0 finding for each directive without a reason or that suppresses nothing.
func applyDirectives(fs []finding, ds []directive) []finding {
	used := make([]bool, len(ds))
	var out []finding
	for _, f := range fs {
		suppressed := false
		for i, d := range ds {
			if d.reason != "" && f.rule == "AP4" && f.file == d.file && f.line == d.line {
				used[i] = true
				suppressed = true
			}
		}
		if !suppressed {
			out = append(out, f)
		}
	}
	for i, d := range ds {
		ap0 := finding{file: d.file, line: d.pos.Line, col: d.pos.Column, rule: "AP0"}
		switch {
		case d.reason == "":
			ap0.msg = "//evmsync:maporder needs a reason"
		case !used[i]:
			ap0.msg = "//evmsync:maporder suppresses no finding"
		default:
			continue
		}
		out = append(out, ap0)
	}
	return out
}

func TestApplyPathRules(t *testing.T) {
	root := testRoot(t)
	var pkgs []pkgDir
	for _, rel := range applyPath {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(dir); err == nil {
			pkgs = append(pkgs, pkgDir{dir: dir, path: importPath(rel)})
		}
	}
	fs, err := checkApplyPath(root, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	report(t, "apply-path rules", fs)
}

func TestApplyPathFixtures(t *testing.T) {
	names := []string{"alias", "clock", "consumer", "directive", "gostmt", "mapsiter", "maprange", "rand"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir, err := filepath.Abs(filepath.Join("testdata", "applypath", name))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(dir, "want.txt"))
			if err != nil {
				t.Fatal(err)
			}
			fs, err := checkApplyPath(dir, []pkgDir{{dir, "fixture/" + name}})
			if err != nil {
				t.Fatal(err)
			}
			equalLines(t, strs(fs), slices.DeleteFunc(strings.Split(string(want), "\n"), func(s string) bool { return s == "" }))
		})
	}
}
