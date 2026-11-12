// Package payment is how a workspace here is paid for (levyan#14, design 7.9,
// D63): `loft.PaymentService`, relayed to levyan -- the billing service this
// deployment's workspaces are billed by -- with the product key, and kept
// nowhere here.
//
// It is the workspace's: who pays for the caller's workspace, its usage and
// estimate this period, its statements, and asking a member to pay for it.
// Served as a membership, relayed to `WorkspaceService` and `BillingService`
// as the product ([levyan.Client.AsProduct]), about the caller's workspace and
// no other -- no request names one. What a payer does with what it pays with
// is the portal's, which relays it to levyan as the payer, and none of this
// deployment's (#326, D81).
//
// # What it answers
//
// What levyan answers, as it is: this package's messages are levyan's own,
// field for field and number for number, so a message is moved by being
// written and read back ([move]) -- and what levyan says that this build does
// not know is left out, since what a page is shown is what this build
// declares.
//
// A refusal is levyan's code with its words after this deployment's
// ([said]). A deployment that names no levyan answers every method
// FailedPrecondition.
package payment

import (
	"context"
	"log/slog"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/levyan"
)

// Service is `loft.PaymentService`.
type Service struct {
	app.UnimplementedPaymentServiceServer

	// Levyan is the billing service: nil on a deployment that bills nobody.
	Levyan *levyan.Client

	// Walled is the stack a caller reaches, walled to the caller's workspace:
	// what an offer reads the member it asks through.
	Walled app.Server
}

var _ app.PaymentServiceServer = Service{}

// ErrNoBilling is every method's answer on a deployment that names no levyan.
var ErrNoBilling = status.Error(codes.FailedPrecondition, "이 배포는 결제를 다루지 않습니다 (청구 서비스 levyan이 설정되지 않았습니다)")

// workspace is the workspace of the membership a call is served as.
func (s Service) workspace(ctx context.Context) (pdid.Id, error) {
	if s.Levyan == nil {
		return pdid.Nil, ErrNoBilling
	}
	f, ok := frame.From(ctx)
	if !ok || f.AsAccount() || f.Tenant == pdid.Nil {
		return pdid.Nil, status.Error(codes.PermissionDenied, "워크스페이스의 구성원으로 부르는 작업입니다")
	}
	return f.Tenant, nil
}

// move is `from` written and read back as `to`: levyan's message as this
// package's, or the other way, which are the same fields under the same
// numbers -- with what `to` does not declare left out.
func move(from, to proto.Message) error {
	b, err := proto.Marshal(from)
	if err != nil {
		return status.Errorf(codes.Internal, "payment: %v", err)
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b, to); err != nil {
		return status.Errorf(codes.Internal, "payment: %v", err)
	}
	return nil
}

// said is a refusal of levyan's in this deployment's words: its code, and
// what levyan said after them -- which is levyan's English, kept for whoever
// is to act on it. A key levyan does not take is the deployment's to fix,
// and nobody's on the page: it is logged, and the page is told no more than
// that.
func said(ctx context.Context, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return status.Error(codes.Unavailable, "결제 서비스에 지금 닿지 않습니다. 잠시 뒤에 다시 해 보세요")
	}
	m := st.Message()
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return status.Errorf(codes.Unavailable, "결제 서비스에 지금 닿지 않습니다. 잠시 뒤에 다시 해 보세요 (%s)", m)
	case codes.Canceled:
		return st.Err()
	case codes.Unauthenticated:
		log.From(ctx).WarnContext(ctx, "levyan: the product key was not taken", slog.String("said", m))
		return status.Error(codes.Internal, "결제 서비스가 이 배포를 받지 않습니다. 운영자에게 알리세요")
	case codes.FailedPrecondition:
		return status.Errorf(codes.FailedPrecondition, "결제 서비스가 받지 않았습니다: %s", m)
	case codes.PermissionDenied:
		return status.Errorf(codes.PermissionDenied, "결제 서비스가 거절했습니다: %s", m)
	case codes.NotFound:
		return status.Errorf(codes.NotFound, "결제 서비스에 없습니다: %s", m)
	case codes.InvalidArgument:
		return status.Errorf(codes.InvalidArgument, "결제 서비스가 받지 않는 값입니다: %s", m)
	default:
		return status.Errorf(st.Code(), "결제 서비스: %s", m)
	}
}

func (s Service) GetPayer(ctx context.Context, req *app.PaymentGetPayerRequest) (*app.PaymentGetPayerResponse, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return nil, err
	}
	call, cancel := s.Levyan.AsProduct(ctx)
	defer cancel()
	v, err := s.Levyan.Workspaces().GetPayer(call, levyanv1.GetPayerRequest_builder{Workspace: ws.String()}.Build())
	if err != nil {
		return nil, said(ctx, err)
	}
	out := &app.PaymentGetPayerResponse{}
	if err := move(v, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) GetUsage(ctx context.Context, req *app.PaymentGetUsageRequest) (*app.PaymentGetUsageResponse, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return nil, err
	}
	call, cancel := s.Levyan.AsProduct(ctx)
	defer cancel()
	v, err := s.Levyan.Billing().GetUsage(call, levyanv1.GetUsageRequest_builder{Workspace: ws.String()}.Build())
	if err != nil {
		return nil, said(ctx, err)
	}
	out := &app.PaymentGetUsageResponse{}
	if err := move(v, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) ListStatements(ctx context.Context, req *app.PaymentListStatementsRequest) (*app.PaymentListStatementsResponse, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return nil, err
	}
	call, cancel := s.Levyan.AsProduct(ctx)
	defer cancel()
	v, err := s.Levyan.Billing().ListStatements(call, levyanv1.ListStatementsRequest_builder{
		Workspace: ws.String(),
		PageSize:  req.GetPageSize(),
		PageToken: req.GetPageToken(),
	}.Build())
	if err != nil {
		return nil, said(ctx, err)
	}
	out := &app.PaymentListStatementsResponse{}
	if err := move(v, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Offer asks a member of the caller's workspace to pay for it: the holder at
// roster the membership is, by its own identifier (D85), read through the
// walled servers -- so a membership of another workspace is none -- and only
// one that is not leaving.
func (s Service) Offer(ctx context.Context, req *app.PaymentOfferRequest) (*app.PaymentOfferResponse, error) {
	ws, err := s.workspace(ctx)
	if err != nil {
		return nil, err
	}
	id, err := pdid.From(req.GetHolder())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "holder: 결제 담당을 제안할 구성원의 자격입니다")
	}
	h, err := s.Walled.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: id.Bytes()}.Build()}.Build())
	switch {
	case status.Code(err) == codes.NotFound:
		return nil, status.Error(codes.InvalidArgument, "holder: 이 워크스페이스의 구성원이 아닙니다")
	case err != nil:
		return nil, err
	}
	if t, err := pdid.From(h.GetTenant().GetId()); err != nil || t != ws {
		return nil, status.Error(codes.InvalidArgument, "holder: 이 워크스페이스의 구성원이 아닙니다")
	}
	if h.HasDateLeaving() {
		return nil, status.Error(codes.FailedPrecondition, "떠나는 중인 구성원에게는 결제 담당을 제안할 수 없습니다")
	}

	call, cancel := s.Levyan.AsProduct(ctx)
	defer cancel()
	v, err := s.Levyan.Workspaces().Offer(call, levyanv1.OfferRequest_builder{Workspace: ws.String(), Account: id.String()}.Build())
	if err != nil {
		return nil, said(ctx, err)
	}
	out := &app.PaymentOfferResponse{}
	if err := move(v, out); err != nil {
		return nil, err
	}
	return out, nil
}
