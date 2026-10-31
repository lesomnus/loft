// Package payment is how a workspace here is paid for (levyan#14, design 7.9,
// D63): `loft.PaymentService`, relayed to levyan -- the billing service this
// deployment's workspaces are billed by -- with the product key, and kept
// nowhere here.
//
// Two halves, as the service says:
//
//   - **The account's own**, a payer's: its billing accounts, the checkout
//     that starts the subscription one is collected through, the offers to
//     pay for a workspace and the workspaces it pays for, its invoices. Served
//     as the account itself (`(payday.method) = {account: {}}`, the policy's
//     `Account`), and relayed to levyan's `PayerService` as that account
//     (`levyan-account`, [levyan.Client.As]): levyan asks roster about it,
//     and answers with what is the account's own and the product's. What each
//     reads through the walled servers is nothing ([pd.AccountReads], an
//     empty `pd.Reads`): every answer is levyan's.
//   - **The workspace's**: who pays for the caller's workspace, its usage and
//     estimate this period, its statements, and asking a member to pay for
//     it. Served as a membership, relayed to `WorkspaceService` and
//     `BillingService` as the product ([levyan.Client.AsProduct]), about the
//     caller's workspace and no other -- no request names one.
//
// # What it answers
//
// What levyan answers, as it is: this package's messages are levyan's own,
// field for field and number for number, so a message is moved by being
// written and read back ([move]) -- and what levyan says that this build does
// not know is left out, since what a page is shown is what this build
// declares. No card is anywhere here: of how a payer pays, levyan answers the
// kind, the brand and the last four digits, and so does this.
//
// A refusal is levyan's code with its words after this deployment's
// ([said]). A deployment that names no levyan answers every method
// FailedPrecondition, and one with no checkout page (`app.billing.checkout`)
// begins no checkout: there is nowhere to open it.
package payment

import (
	"context"
	"log/slog"
	"net/url"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/checkout"
	"github.com/lesomnus/loft/server/levyan"
	"github.com/lesomnus/loft/server/pd"
)

// Service is `loft.PaymentService`.
type Service struct {
	app.UnimplementedPaymentServiceServer

	// Levyan is the billing service: nil on a deployment that bills nobody.
	Levyan *levyan.Client

	// Walled is the stack a caller reaches, walled to the caller's workspace:
	// what an offer reads the member it asks through.
	Walled app.Server

	// Checkout is the page a checkout opens on (`server/checkout`): its
	// environment is the one a checkout levyan begins is to be of.
	Checkout checkout.Config
}

var _ app.PaymentServiceServer = Service{}

// ErrNoBilling is every method's answer on a deployment that names no levyan.
var ErrNoBilling = status.Error(codes.FailedPrecondition, "이 배포는 결제를 다루지 않습니다 (청구 서비스 levyan이 설정되지 않았습니다)")

// account is the account a call is the account's own of.
func (s Service) account(ctx context.Context) (pdid.Id, error) {
	if s.Levyan == nil {
		return pdid.Nil, ErrNoBilling
	}
	f, ok := frame.From(ctx)
	if !ok || !f.AsAccount() || f.Account == pdid.Nil {
		// The policy gives these to an account's own call alone; this is
		// what a layer above it would see were it wired otherwise.
		return pdid.Nil, status.Error(codes.PermissionDenied, "계정 자신으로 부르는 작업입니다")
	}
	return f.Account, nil
}

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

// relay is a call of the account's own, `req` moved into levyan's `in`,
// made of levyan's PayerService as the account, and its answer moved into
// `out`.
func relay[I, O proto.Message](ctx context.Context, s Service, req proto.Message, in I, out proto.Message, call func(levyanv1.PayerServiceClient, context.Context, I, ...grpc.CallOption) (O, error)) error {
	account, err := s.account(ctx)
	if err != nil {
		return err
	}
	if err := move(req, in); err != nil {
		return err
	}
	ctx, cancel := s.Levyan.As(ctx, account)
	defer cancel()
	v, err := call(s.Levyan.Payers(), ctx, in)
	if err != nil {
		return said(ctx, err)
	}
	return move(v, out)
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

func (s Service) ListBillingAccounts(ctx context.Context, req *app.PaymentListBillingAccountsRequest) (*app.PaymentListBillingAccountsResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_ListBillingAccounts_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentListBillingAccountsResponse{}
	if err := relay(ctx, s, req, &levyanv1.ListBillingAccountsRequest{}, out, levyanv1.PayerServiceClient.ListBillingAccounts); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) CreateBillingAccount(ctx context.Context, req *app.PaymentCreateBillingAccountRequest) (*app.PaymentCreateBillingAccountResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_CreateBillingAccount_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentCreateBillingAccountResponse{}
	if err := relay(ctx, s, req, &levyanv1.CreateBillingAccountRequest{}, out, levyanv1.PayerServiceClient.CreateBillingAccount); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) UpdateBillingAccount(ctx context.Context, req *app.PaymentUpdateBillingAccountRequest) (*app.PaymentUpdateBillingAccountResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_UpdateBillingAccount_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentUpdateBillingAccountResponse{}
	if err := relay(ctx, s, req, &levyanv1.UpdateBillingAccountRequest{}, out, levyanv1.PayerServiceClient.UpdateBillingAccount); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) GetBillingAccount(ctx context.Context, req *app.PaymentGetBillingAccountRequest) (*app.PaymentGetBillingAccountResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_GetBillingAccount_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentGetBillingAccountResponse{}
	if err := relay(ctx, s, req, &levyanv1.GetBillingAccountRequest{}, out, levyanv1.PayerServiceClient.GetBillingAccount); err != nil {
		return nil, err
	}
	return out, nil
}

// StartCheckout begins a checkout only where it can be opened: a deployment
// with a checkout page (`app.billing.checkout`), whose environment is the
// one levyan began it in -- Paddle.js initialized for one environment opens
// no checkout of the other's.
func (s Service) StartCheckout(ctx context.Context, req *app.PaymentStartCheckoutRequest) (*app.PaymentStartCheckoutResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_StartCheckout_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	if s.Levyan != nil && !s.Checkout.On() {
		return nil, status.Error(codes.FailedPrecondition, "이 배포에는 결제창을 여는 페이지가 없습니다 (app.billing.checkout)")
	}
	if u, err := url.Parse(req.GetSuccessUrl()); err != nil || !u.IsAbs() {
		return nil, status.Error(codes.InvalidArgument, "success_url: 결제를 마친 뒤 돌아올 이 배포의 주소입니다")
	}
	out := &app.PaymentStartCheckoutResponse{}
	if err := relay(ctx, s, req, &levyanv1.StartCheckoutRequest{}, out, levyanv1.PayerServiceClient.StartCheckout); err != nil {
		return nil, err
	}
	if w := out.GetCheckout().GetWindow(); !s.Checkout.Opens(w.GetEnvironment()) {
		return nil, status.Errorf(codes.FailedPrecondition, "결제창은 Paddle의 %s 환경의 것인데, 이 배포의 결제 페이지는 %s 환경의 것입니다", w.GetEnvironment(), s.Checkout.Environment)
	}
	return out, nil
}

func (s Service) CompleteCheckout(ctx context.Context, req *app.PaymentCompleteCheckoutRequest) (*app.PaymentCompleteCheckoutResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_CompleteCheckout_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentCompleteCheckoutResponse{}
	if err := relay(ctx, s, req, &levyanv1.CompleteCheckoutRequest{}, out, levyanv1.PayerServiceClient.CompleteCheckout); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) ListOffers(ctx context.Context, req *app.PaymentListOffersRequest) (*app.PaymentListOffersResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_ListOffers_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentListOffersResponse{}
	if err := relay(ctx, s, req, &levyanv1.ListOffersRequest{}, out, levyanv1.PayerServiceClient.ListOffers); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) Accept(ctx context.Context, req *app.PaymentAcceptRequest) (*app.PaymentAcceptResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_Accept_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentAcceptResponse{}
	if err := relay(ctx, s, req, &levyanv1.AcceptRequest{}, out, levyanv1.PayerServiceClient.Accept); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) Decline(ctx context.Context, req *app.PaymentDeclineRequest) (*app.PaymentDeclineResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_Decline_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentDeclineResponse{}
	if err := relay(ctx, s, req, &levyanv1.DeclineRequest{}, out, levyanv1.PayerServiceClient.Decline); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) ListLinks(ctx context.Context, req *app.PaymentListLinksRequest) (*app.PaymentListLinksResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_ListLinks_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentListLinksResponse{}
	if err := relay(ctx, s, req, &levyanv1.ListLinksRequest{}, out, levyanv1.PayerServiceClient.ListLinks); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) Use(ctx context.Context, req *app.PaymentUseRequest) (*app.PaymentUseResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_Use_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentUseResponse{}
	if err := relay(ctx, s, req, &levyanv1.UseRequest{}, out, levyanv1.PayerServiceClient.Use); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) Stop(ctx context.Context, req *app.PaymentStopRequest) (*app.PaymentStopResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_Stop_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentStopResponse{}
	if err := relay(ctx, s, req, &levyanv1.StopRequest{}, out, levyanv1.PayerServiceClient.Stop); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) ListInvoices(ctx context.Context, req *app.PaymentListInvoicesRequest) (*app.PaymentListInvoicesResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_ListInvoices_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentListInvoicesResponse{}
	if err := relay(ctx, s, req, &levyanv1.ListInvoicesRequest{}, out, levyanv1.PayerServiceClient.ListInvoices); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) GetInvoice(ctx context.Context, req *app.PaymentGetInvoiceRequest) (*app.PaymentGetInvoiceResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_GetInvoice_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentGetInvoiceResponse{}
	if err := relay(ctx, s, req, &levyanv1.GetInvoiceRequest{}, out, levyanv1.PayerServiceClient.GetInvoice); err != nil {
		return nil, err
	}
	return out, nil
}

func (s Service) ListCreditNotes(ctx context.Context, req *app.PaymentListCreditNotesRequest) (*app.PaymentListCreditNotesResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.PaymentService_ListCreditNotes_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	out := &app.PaymentListCreditNotesResponse{}
	if err := relay(ctx, s, req, &levyanv1.ListCreditNotesRequest{}, out, levyanv1.PayerServiceClient.ListCreditNotes); err != nil {
		return nil, err
	}
	return out, nil
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

// Offer asks a member of the caller's workspace to pay for it: the account
// its membership is of, read through the walled servers -- so a membership of
// another workspace is none -- and only one that is an account's and is not
// leaving.
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
	account, err := pdid.From(h.GetAccount())
	if err != nil || account == pdid.Nil {
		return nil, status.Error(codes.FailedPrecondition, "로그인하는 계정이 없는 구성원에게는 결제 담당을 제안할 수 없습니다")
	}
	if h.HasDateLeaving() {
		return nil, status.Error(codes.FailedPrecondition, "떠나는 중인 계정에게는 결제 담당을 제안할 수 없습니다")
	}

	call, cancel := s.Levyan.AsProduct(ctx)
	defer cancel()
	v, err := s.Levyan.Workspaces().Offer(call, levyanv1.OfferRequest_builder{Workspace: ws.String(), Account: account.String()}.Build())
	if err != nil {
		return nil, said(ctx, err)
	}
	out := &app.PaymentOfferResponse{}
	if err := move(v, out); err != nil {
		return nil, err
	}
	return out, nil
}
