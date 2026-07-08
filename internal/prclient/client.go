// Package prclient is the cairn CLI's outbound client to cairn-server's gRPC
// PullService: the `cairn pr` verbs (open/list/view/merge) dial the server
// directly and carry the caller's identity as cwb-* gRPC metadata, mirroring
// how the gateway injects it for in-cluster callers (see internal/grpcapi's
// authed/identityFromCtx) and how internal/ledger's client forwards it on.
// The channel is mTLS (client cert/key + the cwb-ca, same trio cairn-server
// itself uses to dial ledger/herald) unless the caller opts into an insecure
// dev channel.
package prclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Client calls cairn-server's gRPC PullService.
type Client struct {
	conn  *grpc.ClientConn
	pulls cairnv1.PullServiceClient
}

// NewClient dials addr (e.g. "cairn.cwb.svc:8102" or "127.0.0.1:8102").
// When certFile/keyFile/caFile are all set the connection is mTLS, presenting
// the client cert and trusting the CA to verify the server — the same trio
// cairn-server uses client-side to dial ledger/herald. When they are empty,
// insecureDev must be true (an explicit opt-in, mirrors CAIRN_DEV_INSECURE
// server-side) or NewClient fails closed.
func NewClient(addr, certFile, keyFile, caFile string, insecureDev bool) (*Client, error) {
	var creds credentials.TransportCredentials
	switch {
	case certFile != "" && keyFile != "" && caFile != "":
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("prclient.NewClient: load client cert: %w", err)
		}
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("prclient.NewClient: read CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("prclient.NewClient: parse CA PEM")
		}
		creds = credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool})
	case insecureDev:
		creds = insecure.NewCredentials()
	default:
		return nil, fmt.Errorf("prclient.NewClient: mTLS required — set --tls-cert/--tls-key/--tls-ca (or CAIRN_TLS_CERT/_KEY/_CA), or pass --insecure for local dev")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("prclient.NewClient: dial %s: %w", addr, err)
	}
	return &Client{conn: conn, pulls: cairnv1.NewPullServiceClient(conn)}, nil
}

// NewClientWithConn builds a Client from an already-dialled conn (tests: a
// bufconn/insecure connection without touching the file system).
func NewClientWithConn(conn *grpc.ClientConn) *Client {
	return &Client{conn: conn, pulls: cairnv1.NewPullServiceClient(conn)}
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Identity is the caller identity attached to every call as cwb-* gRPC
// metadata — cairn-server's authed() trusts it on the mTLS channel exactly as
// it trusts the gateway's injected metadata for in-cluster callers.
type Identity struct {
	Subject string
	Org     string
	Scopes  []string
}

// WithIdentity returns ctx carrying id as outgoing cwb-* metadata.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"cwb-subject", id.Subject,
		"cwb-org", id.Org,
		"cwb-scopes", strings.Join(id.Scopes, " "),
	))
}

// Open opens a pull request (idempotent per repo/source/target on the server).
func (c *Client) Open(ctx context.Context, org, slug, source, target, title, description, dod, project string) (*cairnv1.Pull, error) {
	resp, err := c.pulls.OpenPull(ctx, &cairnv1.OpenPullRequest{
		Org: org, Slug: slug, Source: source, Target: target,
		Title: title, Description: description, DefinitionOfDone: dod, Project: project,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetPull(), nil
}

// List lists a repo's pulls, optionally filtered by state ("open", "merged",
// "all"; "" defers to the server default).
func (c *Client) List(ctx context.Context, org, slug, state string) ([]*cairnv1.Pull, error) {
	resp, err := c.pulls.ListPulls(ctx, &cairnv1.ListPullsRequest{Org: org, Slug: slug, State: state})
	if err != nil {
		return nil, err
	}
	return resp.GetPulls(), nil
}

// View fetches a single pull by id.
func (c *Client) View(ctx context.Context, org, slug, id string) (*cairnv1.Pull, error) {
	resp, err := c.pulls.GetPull(ctx, &cairnv1.GetPullRequest{Org: org, Slug: slug, Id: id})
	if err != nil {
		return nil, err
	}
	return resp.GetPull(), nil
}

// Merge fast-forward-merges a pull. A diverged source surfaces the server's
// codes.Aborted "not fast-forwardable; rebase X onto Y" error unchanged.
func (c *Client) Merge(ctx context.Context, org, slug, id string) (*cairnv1.MergeResult, error) {
	resp, err := c.pulls.MergePull(ctx, &cairnv1.MergePullRequest{Org: org, Slug: slug, Id: id})
	if err != nil {
		return nil, err
	}
	return resp.GetResult(), nil
}
