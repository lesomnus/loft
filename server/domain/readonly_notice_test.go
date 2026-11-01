package domain_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/domain"
)

// TestEveryKindASweepSendsIsDecidedForAReadOnlyWorkspace (D67): every kind
// of notification the deployment's sweeps may send says in
// [domain.ReadOnlyTells] whether a workspace levyan makes read-only is told
// it while it is, or once it writes again -- so a kind a sweep is given later
// is decided as it is added, and not held back, or told, unseen. And every
// kind the table says is one a sweep sends.
//
// Read off this package's source, as `readonly_test.go` reads it: from each
// sweep `sweeps` names, through what it calls of this package by name, to
// every notice written ([Tx.notify] and the three that call it), by the kind
// it is written as -- a literal, or the test cannot say which it is.
func TestEveryKindASweepSendsIsDecidedForAReadOnlyWorkspace(t *testing.T) {
	src := parse(t)
	roots := sweepsOf(t)
	require.NotEmpty(t, roots, "no sweep was read: the test reads nothing")
	x := assert.New(t)

	sent := map[string][]string{}
	for _, root := range roots {
		fds := src.byName[root]
		require.NotEmpty(t, fds, "%s is a sweep this package does not declare", root)
		for _, fd := range fds {
			for _, k := range src.notices(fd) {
				if !slices.Contains(sent[k], root) {
					sent[k] = append(sent[k], root)
				}
			}
		}
	}
	require.NotEmpty(t, sent, "no notice was read: the test reads nothing")
	for k, by := range sent {
		x.NotEmpty(k, "a notice %s writes is of a kind the test cannot read: write it as a literal", strings.Join(by, ", "))
		if k == "" {
			continue
		}
		_, ok := domain.ReadOnlyTells[k]
		x.True(ok, "%s, which %s sends, is not in domain.ReadOnlyTells: a read-only workspace is not told it, and nothing tells it once it writes again -- "+
			"say there TellsStill if it asks nobody to do anything, or TellsOnceWritable if it does, and have the sweep tell it once the workspace writes again", k, strings.Join(by, ", "))
	}
	for k, v := range domain.ReadOnlyTells {
		_, ok := sent[k]
		x.True(ok, "%s is in domain.ReadOnlyTells and no sweep sends it", k)
		x.Contains([]domain.Telling{domain.TellsStill, domain.TellsOnceWritable}, v, k)
	}
}

// sweepsOf is the functions this package's `sweeps` names, read off its
// source.
func sweepsOf(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	out := []string{}
	fs := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fs, f, nil, 0)
		require.NoError(t, err)
		for _, d := range af.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, sp := range g.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if n.Name != "sweeps" || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.CompositeLit)
					require.True(t, ok, "sweeps is no list the test can read")
					for _, el := range lit.Elts {
						s, ok := el.(*ast.CompositeLit)
						require.True(t, ok, "a sweep is no literal the test can read")
						require.Len(t, s.Elts, 2, "a sweep is its name and its function")
						id, ok := s.Elts[1].(*ast.Ident)
						require.True(t, ok, "a sweep's function is no name the test can read")
						out = append(out, id.Name)
					}
				}
			}
		}
	}
	return out
}

// noticeWriters is where a notice is written, by the argument it says its kind in.
var noticeWriters = map[string]int{"notify": 1, "notifyParty": 1, "notifyManagers": 0, "notifyAdmins": 0}

// notices answers the kind of every notice `fd`, or what of this package it
// calls by name, writes -- a call is followed as [source.reaches] follows one
// -- and an empty one for each it writes by anything but a literal.
func (s *source) notices(fd *ast.FuncDecl) []string {
	out := []string{}
	seen := map[*ast.FuncDecl]bool{}
	var visit func(*ast.FuncDecl)
	visit = func(fd *ast.FuncDecl) {
		if seen[fd] {
			return
		}
		seen[fd] = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := callee(c.Fun)
			if i, ok := noticeWriters[name]; ok {
				kind := ""
				if i < len(c.Args) {
					if lit, ok := c.Args[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, err := strconv.Unquote(lit.Value)
						if err == nil {
							kind = v
						}
					}
				}
				if !slices.Contains(out, kind) {
					out = append(out, kind)
				}
				return true
			}
			if name == "" || ast.IsExported(name) {
				return true
			}
			for _, d := range s.byName[name] {
				visit(d)
			}
			return true
		})
	}
	visit(fd)
	return out
}
