// Package ledger is cairn's outbound client to the CWB issues/tracker service.
// cairn calls it in-cluster (ledger.cwb.svc) over gRPC + mTLS to open a
// tracking issue when a pull request is opened, FORWARDING the
// gateway-injected X-CWB-* identity of the opener as cwb-* gRPC metadata so
// the issue is created on their behalf. Mirrors internal/herald.
package ledger

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"

	cwbv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Client calls ledger's gRPC IssueService.
type Client struct {
	conn   *grpc.ClientConn
	issues cwbv1.IssueServiceClient
}

// NewClient dials ledger over mTLS (cairn presents its client cert, trusts the
// cwb-ca) and returns a ready Client. addr is e.g. "ledger.cwb.svc:8081".
func NewClient(addr, certFile, keyFile, caFile string) (*Client, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("ledger.NewClient: load client cert: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("ledger.NewClient: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("ledger.NewClient: parse CA PEM")
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("ledger.NewClient: dial %s: %w", addr, err)
	}
	return &Client{conn: conn, issues: cwbv1.NewIssueServiceClient(conn)}, nil
}

// NewClientWithConn builds a Client from an already-dialled conn. Used in
// tests to inject a bufconn/insecure connection without touching the file
// system. Exported so the _test package and cmd/cairn-server can use it if
// needed; production code should use NewClient.
func NewClientWithConn(conn *grpc.ClientConn) *Client {
	return &Client{conn: conn, issues: cwbv1.NewIssueServiceClient(conn)}
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ExternalRef links the issue back to the cairn branch/PR.
type ExternalRef struct {
	Tracker     string
	Key         string
	URL         string
	Description string
}

// IssueInput is the create-issue request (cairn's view).
type IssueInput struct {
	Project          string
	Type             string
	Summary          string
	Description      string
	DefinitionOfDone string
	ExternalRefs     []ExternalRef
}

// IssueResult is the decoded create-issue response (the parts cairn needs).
type IssueResult struct {
	Key string
}

// APIError wraps a gRPC status error so callers can check error category.
// The Status field carries the gRPC status code (as int32) and Message the
// human-readable detail. The handler in pulls.go does errors.As on *APIError
// to mirror the HTTP status back — with gRPC the exact HTTP status is gone, so
// we map codes.PermissionDenied→403, codes.InvalidArgument→400, else 502.
type APIError struct {
	Status  int
	Body    string
	GRPCErr error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ledger: grpc status %d: %s", e.Status, e.Body)
}

func (e *APIError) Unwrap() error { return e.GRPCErr }

// grpcStatusToAPIError converts a non-nil gRPC error to *APIError with an
// approximate HTTP status for the existing caller error-mapping in pulls.go.
func grpcStatusToAPIError(err error) *APIError {
	st, _ := status.FromError(err)
	httpStatus := 502
	switch st.Code() {
	case 7: // codes.PermissionDenied
		httpStatus = 403
	case 3: // codes.InvalidArgument
		httpStatus = 400
	case 5: // codes.NotFound
		httpStatus = 404
	case 6: // codes.AlreadyExists
		httpStatus = 409
	}
	return &APIError{Status: httpStatus, Body: st.Message(), GRPCErr: err}
}

// cwbHeaderToMeta maps X-CWB-* header names to their cwb-* metadata key.
var cwbHeaderToMeta = []struct{ header, key string }{
	{"X-Cwb-Org", "cwb-org"},
	{"X-Cwb-Subject", "cwb-subject"},
	{"X-Cwb-Kind", "cwb-kind"},
	{"X-Cwb-Scopes", "cwb-scopes"},
	{"X-Cwb-Responsible-Human", "cwb-responsible-human"},
}

// mdFromForwarded converts the X-CWB-* headers cairn received from the
// gateway into outgoing gRPC metadata for the ledger call.
func mdFromForwarded(fwd http.Header) metadata.MD {
	md := metadata.MD{}
	for _, m := range cwbHeaderToMeta {
		if v := fwd.Get(m.header); v != "" {
			md[m.key] = []string{v}
		}
	}
	return md
}

// CreateIssue calls IssueService.CreateIssue with the forwarded identity. A
// gRPC error is returned as *APIError; a transport failure as a wrapped error.
func (c *Client) CreateIssue(ctx context.Context, fwd http.Header, in IssueInput) (IssueResult, error) {
	md := mdFromForwarded(fwd)
	mdCtx := metadata.NewOutgoingContext(ctx, md)

	refs := make([]*cwbv1.ExternalRef, len(in.ExternalRefs))
	for i, r := range in.ExternalRefs {
		refs[i] = &cwbv1.ExternalRef{
			Tracker:     r.Tracker,
			Key:         r.Key,
			Url:         r.URL,
			Description: r.Description,
		}
	}

	req := &cwbv1.CreateIssueRequest{
		Project:          in.Project,
		Type:             strings.TrimSpace(in.Type),
		Summary:          in.Summary,
		Description:      in.Description,
		DefinitionOfDone: in.DefinitionOfDone,
		ExternalRefs:     refs,
	}

	resp, err := c.issues.CreateIssue(mdCtx, req)
	if err != nil {
		return IssueResult{}, grpcStatusToAPIError(err)
	}
	if resp.Issue == nil || resp.Issue.Key == "" {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: empty key in response")
	}
	return IssueResult{Key: resp.Issue.Key}, nil
}

// CommentIssue calls IssueService.CommentIssue with the forwarded identity.
// A gRPC error is returned as *APIError; a transport failure as a wrapped error.
// Callers (the merge handler) treat both as best-effort.
func (c *Client) CommentIssue(ctx context.Context, fwd http.Header, key, body string) error {
	md := mdFromForwarded(fwd)
	mdCtx := metadata.NewOutgoingContext(ctx, md)

	req := &cwbv1.CommentIssueRequest{
		Key:  key,
		Body: body,
	}

	_, err := c.issues.CommentIssue(mdCtx, req)
	if err != nil {
		return grpcStatusToAPIError(err)
	}
	return nil
}
