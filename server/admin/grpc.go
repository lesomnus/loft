package admin

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// Grpc is the server every call to the entry point arrives at.
//
// The chain is read in the order a call travels it: what records the call and
// recovers from a panic (payday's), who is calling -- an operator's key, or
// nothing is read -- what the access record is told of it, and the table of
// what an operator may call. It is a server of its own rather than the public
// one with a rule added, so that nothing of it is in the public binary at all.
//
// It is separate from [Routes.Handler] so that a test can travel exactly this
// over a channel; see pdtest.
func (a Routes) Grpc(ctx context.Context, opts ...grpc.ServerOption) (*grpc.Server, error) {
	chain := grpcx.Serving(ctx, grpcx.WithDeadline(a.C.Server.CallTimeout())).
		WithUnary(a.authUnary()).
		WithStream(a.authStream()).
		With(a.accessed().Interceptor()).
		WithUnary(mayUnary).
		WithStream(mayStream)

	g := grpc.NewServer(append(opts, chain.ServerOptions()...)...)
	app.RegisterTenantServiceServer(g, tenants{a: a})
	app.RegisterTenantContractServiceServer(g, contracts{a: a})
	app.RegisterLegalHoldServiceServer(g, holds{a: a})
	app.RegisterHolderServiceServer(g, holders{a: a})
	app.RegisterOperatorTenantServiceServer(g, operatorTenants{a: a})
	app.RegisterOperatorTrailServiceServer(g, operatorTrail{a: a})
	app.RegisterOperatorSearchServiceServer(g, operatorSearch{a: a})
	app.RegisterOperatorRetentionServiceServer(g, operatorRetention{a: a})
	return g, nil
}

// who is the operator a call's key is held by.
func (a Routes) who(ctx context.Context) (operator.Operator, bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		key, ok := strings.CutPrefix(strings.TrimSpace(v), "Bearer ")
		if !ok {
			continue
		}
		if o, ok := holding(a.Operators, key); ok {
			return o, true
		}
	}
	return operator.Operator{}, false
}

// errWho is what a call without an operator's key is answered, before anything
// is read for it.
var errWho = status.Error(codes.Unauthenticated, "who is asking? the operator's entry point answers an operator's key (admin.keys)")

// called says in the log who made a call and what came of it: the operator by
// name, which no row of the deployment carries.
func called(ctx context.Context, who, method string, err error) {
	log.From(ctx).InfoContext(ctx, "admin", slog.String("operator", who), slog.String("method", method), slog.String("code", status.Code(err).String()))
}

func (a Routes) authUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		o, ok := a.who(ctx)
		if !ok {
			log.From(ctx).WarnContext(ctx, "admin: a call without an operator's key", slog.String("addr", access.Peer(ctx)), slog.String("method", info.FullMethod))
			return nil, errWho
		}
		res, err := handler(operator.Into(ctx, o), req)
		called(ctx, o.Name, info.FullMethod, err)
		return res, err
	}
}

func (a Routes) authStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		o, ok := a.who(ctx)
		if !ok {
			log.From(ctx).WarnContext(ctx, "admin: a call without an operator's key", slog.String("addr", access.Peer(ctx)), slog.String("method", info.FullMethod))
			return errWho
		}
		err := handler(srv, &streamWith{ServerStream: ss, ctx: operator.Into(ctx, o)})
		called(ctx, o.Name, info.FullMethod, err)
		return err
	}
}

// streamWith is a stream whose context carries what an interceptor put in it.
type streamWith struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *streamWith) Context() context.Context { return s.ctx }

// accessed is what the access record is told of the entry point (loft#136,
// payday#206): whose data a message carries, read off the schema as the
// public server reads it; the operator, where no frame names anybody -- what
// a read is, here -- with no workspace they are held by; where the call came
// from, through the entry point; which calls are downloads; and every read's
// row written before its answer leaves, since nothing here writes them a
// second later and nothing read here may go unrecorded.
func (a Routes) accessed() access.Config {
	return access.Config{
		Subjects:  pd.Subjects,
		Refs:      pd.SubjectRefs,
		Address:   func(ctx context.Context) string { return "admin:" + access.Peer(ctx) },
		Downloads: Downloads(),
		Writer:    a.S.Access,
		Operator: func(ctx context.Context) (pdid.Id, pdid.Id) {
			return operator.Actor(ctx), pdid.Nil
		},
		AtOnce: true,
	}
}
