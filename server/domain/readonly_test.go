package domain_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/domain"
	"github.com/lesomnus/loft/server/policy"
)

// TestReadOnlyRefusesEveryWriteItDoesNotAllow (design 7.10, D64): every
// method `server/policy` may give a role is one a read-only workspace still
// does -- it is in [domain.ReadOnlyAllows] -- or a write it refuses: one this
// layer serves itself, in a transaction of its own, which a read-only
// workspace commits only for what the table allows (`Deps.run`). So a write
// somebody adds later is refused until it is put in the table, and none
// slips past it unseen: one this layer does not serve -- a generated Erase
// given to a role as it is -- or serves outside a transaction would be
// written by a read-only workspace, and is said here instead. And what the
// table allows as a read is held to writing nothing: none of it opens a
// transaction.
//
// Read off this package's source, as `pd doctor` reads the wiring: a method
// is served here when the type its service's accessor answers
// (`func (s Domain) Asset() app.AssetServiceServer`) declares it, and writes
// in a transaction when it -- or a function of this package it calls, by
// name -- opens one (`tx`, `run`, `System`). The general writes, `Patch` and
// `Apply`, are no caller's to ask for at all (payday's `grpcx.Closed`), and
// a batch holds each of its operations to the table by itself.
func TestReadOnlyRefusesEveryWriteItDoesNotAllow(t *testing.T) {
	src := parse(t)
	require.NotEmpty(t, src.served, "no service accessor was read: the test reads nothing")
	// Every one that is not as it should be, rather than the first.
	x := assert.New(t)

	writes := []string{"tx", "run", "System"}
	for m := range policy.Table {
		if m == pdpb.BatchService_Do_FullMethodName || grpcx.GeneralWrite(m) {
			continue
		}
		svc, name, ok := strings.Cut(strings.TrimPrefix(m, "/loft."), "/")
		require.True(t, ok, m)
		decl := src.method(svc, name)

		why, allowed := domain.ReadOnlyAllows[m]
		if allowed {
			if why == domain.Reads && decl != nil {
				x.False(src.reaches(decl, append(writes, "begin")...), "%s is allowed a read-only workspace as a read (domain.Reads), and opens a transaction: say why it writes", m)
			}
			continue
		}
		if !x.NotNil(decl, "%s is not in domain.ReadOnlyAllows, and this layer does not serve it, so nothing refuses it: "+
			"a read is said there as one (domain.Reads), and a write is served here, in a transaction (Domain.tx)", m) {
			continue
		}
		x.True(src.reaches(decl, writes...), "%s is not in domain.ReadOnlyAllows, and this layer serves it in no transaction of its own, so nothing refuses it: "+
			"a read is said there as one (domain.Reads), and a write is written in one (Domain.tx)", m)
	}

	for m := range domain.ReadOnlyAllows {
		_, ok := policy.Table[m]
		x.True(ok, "%s is no method anybody may call", m)
	}
}

// source is this package's functions and methods, as written.
type source struct {
	// byName is every function and method by its name alone: what a call
	// of that name may be.
	byName map[string][]*ast.FuncDecl
	// methods is every method by its receiver's type and its name.
	methods map[string]*ast.FuncDecl
	// served is the type each service's accessor answers, by the service.
	served map[string]string
}

func parse(t *testing.T) *source {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	s := &source{byName: map[string][]*ast.FuncDecl{}, methods: map[string]*ast.FuncDecl{}, served: map[string]string{}}
	fs := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fs, f, nil, 0)
		require.NoError(t, err)
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			s.byName[fd.Name.Name] = append(s.byName[fd.Name.Name], fd)
			recv := receiver(fd)
			if recv == "" {
				continue
			}
			s.methods[recv+"."+fd.Name.Name] = fd
			if recv == "Domain" {
				if svc, typ, ok := accessor(fd); ok {
					s.served[svc] = typ
				}
			}
		}
	}
	return s
}

// method is what this layer declares of `name` on the type that serves
// `svc`, or nil for a method it leaves to the servers below.
func (s *source) method(svc, name string) *ast.FuncDecl {
	typ, ok := s.served[svc]
	if !ok {
		return nil
	}
	return s.methods[typ+"."+name]
}

// reaches is whether `fd`, or what of this package it calls, calls one of
// `names`. A call is followed by its name to every function and method of
// this package that has it, unexported ones alone: an exported method is an
// RPC of another service, which answers for itself.
func (s *source) reaches(fd *ast.FuncDecl, names ...string) bool {
	seen := map[*ast.FuncDecl]bool{}
	var visit func(*ast.FuncDecl) bool
	visit = func(fd *ast.FuncDecl) bool {
		if seen[fd] {
			return false
		}
		seen[fd] = true
		found := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if found {
				return false
			}
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := callee(c.Fun)
			if slices.Contains(names, name) {
				found = true
				return false
			}
			if name == "" || ast.IsExported(name) {
				return true
			}
			for _, d := range s.byName[name] {
				if visit(d) {
					found = true
					return false
				}
			}
			return true
		})
		return found
	}
	return visit(fd)
}

// callee is the name a call is made by: `f(…)`, `x.f(…)` and `f[T](…)`.
func callee(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.IndexExpr:
		return callee(v.X)
	case *ast.IndexListExpr:
		return callee(v.X)
	}
	return ""
}

// receiver is the name of the type `fd` is a method of, or empty.
func receiver(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	e := fd.Recv.List[0].Type
	if p, ok := e.(*ast.StarExpr); ok {
		e = p.X
	}
	if i, ok := e.(*ast.Ident); ok {
		return i.Name
	}
	return ""
}

// accessor reads `func (s Domain) Asset() app.AssetServiceServer { return
// domainAsset{…} }`: the service, and the type that serves it.
func accessor(fd *ast.FuncDecl) (svc, typ string, ok bool) {
	if fd.Type.Params.NumFields() != 0 || fd.Type.Results.NumFields() != 1 {
		return "", "", false
	}
	sel, ok := fd.Type.Results.List[0].Type.(*ast.SelectorExpr)
	if !ok || !strings.HasSuffix(sel.Sel.Name, "ServiceServer") {
		return "", "", false
	}
	svc = strings.TrimSuffix(sel.Sel.Name, "Server")
	for _, st := range fd.Body.List {
		r, ok := st.(*ast.ReturnStmt)
		if !ok || len(r.Results) != 1 {
			continue
		}
		if cl, ok := r.Results[0].(*ast.CompositeLit); ok {
			if id, ok := cl.Type.(*ast.Ident); ok {
				return svc, id.Name, true
			}
		}
	}
	return "", "", false
}
