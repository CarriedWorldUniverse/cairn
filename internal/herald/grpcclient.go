package herald

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	heraldv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/herald/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// GRPCClient is the gRPC HeraldAgents: it resolves a casket fingerprint to an
// agent via herald's AgentService (GetAgentByFingerprint) over mTLS, dialed
// DIRECTLY in-cluster. The SSH flow presents a pubkey, not a token, so this is
// an mTLS-authenticated internal service call — never routed through
// interchange's JWT edge (Phase 4).
type GRPCClient struct {
	conn   *grpc.ClientConn
	client heraldv1.AgentServiceClient
}

// NewGRPCClient dials herald's gRPC at addr (e.g. "herald.cwb.svc:8098") with
// the cwb-ca mTLS client cert.
func NewGRPCClient(addr, certFile, keyFile, caFile string) (*GRPCClient, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("herald grpc: load cert/key: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("herald grpc: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("herald grpc: no certs parsed from CA %s", caFile)
	}
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("herald grpc: dial %s: %w", addr, err)
	}
	return &GRPCClient{conn: conn, client: heraldv1.NewAgentServiceClient(conn)}, nil
}

// NewClientWithConn wraps an existing connection (tests / shared conns).
func NewClientWithConn(conn *grpc.ClientConn) *GRPCClient {
	return &GRPCClient{conn: conn, client: heraldv1.NewAgentServiceClient(conn)}
}

// Close releases the gRPC connection.
func (c *GRPCClient) Close() error { return c.conn.Close() }

// LookupByFingerprint implements HeraldAgents over gRPC.
func (c *GRPCClient) LookupByFingerprint(ctx context.Context, fp string) (Agent, error) {
	resp, err := c.client.GetAgentByFingerprint(ctx, &heraldv1.GetAgentByFingerprintRequest{Fingerprint: fp})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Agent{}, ErrAgentNotFound
		}
		return Agent{}, fmt.Errorf("herald.LookupByFingerprint: %w", err)
	}
	a := resp.GetAgent()
	return Agent{
		ID:          a.GetId(),
		OrgID:       a.GetOrg(),
		Active:      a.GetActive(),
		Scopes:      a.GetScopes(),
		Fingerprint: fp,
	}, nil
}
