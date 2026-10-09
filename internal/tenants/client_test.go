// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tenants

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"golang.org/x/oauth2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/monitoring"
	"github.com/canonical/sso-service/internal/tracing"
)

// fakeTenantService records what it was asked and with which token.
type fakeTenantService struct {
	v0tenant.UnimplementedTenantSignInServiceServer
	v0tenant.UnimplementedTenantSSOPolicyServiceServer

	mu      sync.Mutex
	signIn  *v0tenant.GetSignInContextRequest
	get     *v0tenant.GetTenantSSOPolicyRequest
	put     *v0tenant.PutTenantSSOPolicyRequest
	domains *v0tenant.SetTenantSSODomainsRequest
	unbind  *v0tenant.RemoveTenantSSOBindingRequest
	context *v0tenant.SignInContext
	// policy is the tenant's policy as every policy RPC answers it.
	policy *v0tenant.TenantSSOPolicy
	err    error

	// failures are answered first, one per call, before err or success.
	failures []error
	// tokens is the authorization each call came with, retries included.
	tokens []string
	// deadline is how long the last call had left when it arrived.
	deadline time.Duration
	// hang makes every call wait until the caller gives up.
	hang bool
}

// seen records a call and returns what it is to be answered with.
func (f *fakeTenantService) seen(ctx context.Context) error {
	f.mu.Lock()
	hang := f.hang
	authorization := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok && len(md.Get("authorization")) == 1 {
		authorization = md.Get("authorization")[0]
	}
	f.tokens = append(f.tokens, authorization)
	f.deadline = 0
	if at, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(at)
	}
	err := f.err
	if len(f.failures) > 0 {
		err, f.failures = f.failures[0], f.failures[1:]
	}
	f.mu.Unlock()

	if hang {
		<-ctx.Done()
		return status.FromContextError(ctx.Err()).Err()
	}
	return err
}

// reached is how many calls arrived, retries included.
func (f *fakeTenantService) reached() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokens)
}

func (f *fakeTenantService) GetSignInContext(ctx context.Context, r *v0tenant.GetSignInContextRequest) (*v0tenant.GetSignInContextResponse, error) {
	if err := f.seen(ctx); err != nil {
		return nil, err
	}
	f.signIn = r
	return &v0tenant.GetSignInContextResponse{Context: f.context}, nil
}

func (f *fakeTenantService) GetTenantSSOPolicy(ctx context.Context, r *v0tenant.GetTenantSSOPolicyRequest) (*v0tenant.GetTenantSSOPolicyResponse, error) {
	if err := f.seen(ctx); err != nil {
		return nil, err
	}
	f.get = r
	return &v0tenant.GetTenantSSOPolicyResponse{Policy: f.policy}, nil
}

func (f *fakeTenantService) PutTenantSSOPolicy(ctx context.Context, r *v0tenant.PutTenantSSOPolicyRequest) (*v0tenant.PutTenantSSOPolicyResponse, error) {
	if err := f.seen(ctx); err != nil {
		return nil, err
	}
	f.put = r
	return &v0tenant.PutTenantSSOPolicyResponse{Policy: f.policy}, nil
}

func (f *fakeTenantService) SetTenantSSODomains(ctx context.Context, r *v0tenant.SetTenantSSODomainsRequest) (*v0tenant.SetTenantSSODomainsResponse, error) {
	if err := f.seen(ctx); err != nil {
		return nil, err
	}
	f.domains = r
	return &v0tenant.SetTenantSSODomainsResponse{Policy: f.policy}, nil
}

func (f *fakeTenantService) RemoveTenantSSOBinding(ctx context.Context, r *v0tenant.RemoveTenantSSOBindingRequest) (*v0tenant.RemoveTenantSSOBindingResponse, error) {
	if err := f.seen(ctx); err != nil {
		return nil, err
	}
	f.unbind = r
	return &v0tenant.RemoveTenantSSOBindingResponse{}, nil
}

// serve starts the fake tenant-service on a local listener.
func serve(t *testing.T, f *fakeTenantService) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v0tenant.RegisterTenantSignInServiceServer(server, f)
	v0tenant.RegisterTenantSSOPolicyServiceServer(server, f)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	return listener.Addr().String()
}

const callTimeout = 3 * time.Second

var token = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "svc-token"})

// dial is a client of f with the given timeout per call, connected as the
// service connects it.
func dial(t *testing.T, f *fakeTenantService, source oauth2.TokenSource, timeout time.Duration) *Client {
	t.Helper()
	conn, err := NewGRPCConn(serve(t, f), false, DialOptions(source)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	logger := logging.NewNoopLogger()

	return NewClient(v0tenant.NewTenantSignInServiceClient(conn), v0tenant.NewTenantSSOPolicyServiceClient(conn), timeout,
		tracing.NewNoopTracer(), monitoring.NewNoopMonitor("sso-service", logger), logger)
}

// refusal is tenant-service refusing a request with a reason.
func refusal(t *testing.T, code codes.Code, reason string) error {
	t.Helper()
	st, err := status.New(code, reason+": refused").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "tenant-service"})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestClient_GetSignInContext(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeTenantService{context: &v0tenant.SignInContext{Member: true, ConnectionIds: []string{"c1"}}}

		got, err := dial(t, f, token, callTimeout).GetSignInContext(context.Background(), "t", "", "i")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !proto.Equal(got, f.context) {
			t.Errorf("expected tenant-service's context, got %v", got)
		}
		if f.signIn.GetTenantId() != "t" || f.signIn.GetEmail() != "" || f.signIn.GetIdentityId() != "i" {
			t.Errorf("unexpected request %v", f.signIn)
		}
	})

	t.Run("by email", func(t *testing.T) {
		f := &fakeTenantService{context: &v0tenant.SignInContext{}}

		if _, err := dial(t, f, token, callTimeout).GetSignInContext(context.Background(), "t", "a@b.example", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.signIn.GetEmail() != "a@b.example" || f.signIn.GetIdentityId() != "" {
			t.Errorf("unexpected request %v", f.signIn)
		}
	})

	t.Run("no context", func(t *testing.T) {
		f := &fakeTenantService{}

		if _, err := dial(t, f, token, callTimeout).GetSignInContext(context.Background(), "t", "", "i"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
	})

	t.Run("tenant not found", func(t *testing.T) {
		f := &fakeTenantService{err: status.Error(codes.NotFound, "tenant not found")}

		if _, err := dial(t, f, token, callTimeout).GetSignInContext(context.Background(), "t", "", "i"); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("expected %v, got %v", ErrTenantNotFound, err)
		}
	})
}

func TestClient_GetTenantSSOPolicy(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeTenantService{policy: &v0tenant.TenantSSOPolicy{TenantId: "t", Enforcement: v0tenant.Enforcement_ENFORCEMENT_OPTIONAL}}

		policy, err := dial(t, f, token, callTimeout).GetTenantSSOPolicy(context.Background(), "t")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.get.GetTenantId() != "t" {
			t.Errorf("unexpected request %v", f.get)
		}
		if !proto.Equal(policy, f.policy) {
			t.Errorf("expected tenant-service's policy, got %v", policy)
		}
	})

	t.Run("personal tenant", func(t *testing.T) {
		f := &fakeTenantService{err: refusal(t, codes.FailedPrecondition, "PERSONAL_TENANT")}

		if _, err := dial(t, f, token, callTimeout).GetTenantSSOPolicy(context.Background(), "t"); !errors.Is(err, ErrPersonalTenant) {
			t.Errorf("expected %v, got %v", ErrPersonalTenant, err)
		}
	})

	t.Run("tenant not found", func(t *testing.T) {
		f := &fakeTenantService{err: status.Error(codes.NotFound, "tenant not found")}

		if _, err := dial(t, f, token, callTimeout).GetTenantSSOPolicy(context.Background(), "t"); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("expected %v, got %v", ErrTenantNotFound, err)
		}
	})
}

func TestClient_PutTenantSSOPolicy(t *testing.T) {
	request := &v0tenant.PutTenantSSOPolicyRequest{TenantId: "t", Enforcement: v0tenant.Enforcement_ENFORCEMENT_REQUIRED, AutoJoin: true,
		Bindings: []*v0tenant.SSOBinding{{ConnectionId: "c1", Active: true}, {ConnectionId: "c2"}}}

	t.Run("success", func(t *testing.T) {
		f := &fakeTenantService{policy: &v0tenant.TenantSSOPolicy{TenantId: "t", Enforcement: v0tenant.Enforcement_ENFORCEMENT_REQUIRED,
			AutoJoin: true, Domains: []string{"acme.example"}, Bindings: request.GetBindings()}}

		policy, err := dial(t, f, token, callTimeout).PutTenantSSOPolicy(context.Background(), request)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !proto.Equal(f.put, request) {
			t.Errorf("unexpected request %v", f.put)
		}
		if !proto.Equal(policy, f.policy) {
			t.Errorf("expected the policy as tenant-service stored it, got %v", policy)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := &fakeTenantService{err: refusal(t, codes.FailedPrecondition, "REQUIRED_NEEDS_ACTIVE_BINDING")}

		policy, err := dial(t, f, token, callTimeout).PutTenantSSOPolicy(context.Background(), request)
		if !errors.Is(err, ErrRequiredNeedsActiveBinding) || policy != nil {
			t.Errorf("expected %v and no policy, got %v %v", ErrRequiredNeedsActiveBinding, err, policy)
		}
	})
}

func TestClient_SetTenantSSODomains(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeTenantService{policy: &v0tenant.TenantSSOPolicy{TenantId: "t", Domains: []string{"a.example", "b.example"}}}

		policy, err := dial(t, f, token, callTimeout).SetTenantSSODomains(context.Background(), "t", []string{"a.example", "b.example"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.domains.GetTenantId() != "t" || !slices.Equal(f.domains.GetDomains(), []string{"a.example", "b.example"}) {
			t.Errorf("unexpected request %v", f.domains)
		}
		if !proto.Equal(policy, f.policy) {
			t.Errorf("expected the policy as tenant-service stored it, got %v", policy)
		}
	})

	t.Run("no domains", func(t *testing.T) {
		f := &fakeTenantService{policy: &v0tenant.TenantSSOPolicy{TenantId: "t"}}

		if _, err := dial(t, f, token, callTimeout).SetTenantSSODomains(context.Background(), "t", nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.domains.GetTenantId() != "t" || len(f.domains.GetDomains()) != 0 {
			t.Errorf("unexpected request %v", f.domains)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := &fakeTenantService{err: refusal(t, codes.FailedPrecondition, "AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS")}

		if _, err := dial(t, f, token, callTimeout).SetTenantSSODomains(context.Background(), "t", nil); !errors.Is(err, ErrAutoJoinNeedsRequiredAndDomains) {
			t.Errorf("expected %v, got %v", ErrAutoJoinNeedsRequiredAndDomains, err)
		}
	})
}

func TestClient_RemoveTenantSSOBinding(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := &fakeTenantService{}

		if err := dial(t, f, token, callTimeout).RemoveTenantSSOBinding(context.Background(), "t", "c1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.unbind.GetTenantId() != "t" || f.unbind.GetConnectionId() != "c1" {
			t.Errorf("unexpected request %v", f.unbind)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := &fakeTenantService{err: refusal(t, codes.FailedPrecondition, "REQUIRED_NEEDS_ACTIVE_BINDING")}

		err := dial(t, f, token, callTimeout).RemoveTenantSSOBinding(context.Background(), "t", "c1")
		if !errors.Is(err, ErrRequiredNeedsActiveBinding) {
			t.Errorf("expected %v, got %v", ErrRequiredNeedsActiveBinding, err)
		}
	})

	t.Run("tenant not found", func(t *testing.T) {
		f := &fakeTenantService{err: status.Error(codes.NotFound, "tenant not found")}

		if err := dial(t, f, token, callTimeout).RemoveTenantSSOBinding(context.Background(), "t", "c1"); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("expected %v, got %v", ErrTenantNotFound, err)
		}
	})
}

// Every call is bounded by the client's timeout, whatever its caller's
// context says.
func TestClient_Timeout(t *testing.T) {
	// calls are every method of the client.
	calls := map[string]func(ctx context.Context, c *Client) error{
		"GetSignInContext": func(ctx context.Context, c *Client) error {
			_, err := c.GetSignInContext(ctx, "t", "", "i")
			return err
		},
		"GetTenantSSOPolicy": func(ctx context.Context, c *Client) error {
			_, err := c.GetTenantSSOPolicy(ctx, "t")
			return err
		},
		"PutTenantSSOPolicy": func(ctx context.Context, c *Client) error {
			_, err := c.PutTenantSSOPolicy(ctx, &v0tenant.PutTenantSSOPolicyRequest{TenantId: "t"})
			return err
		},
		"SetTenantSSODomains": func(ctx context.Context, c *Client) error {
			_, err := c.SetTenantSSODomains(ctx, "t", nil)
			return err
		},
		"RemoveTenantSSOBinding": func(ctx context.Context, c *Client) error {
			return c.RemoveTenantSSOBinding(ctx, "t", "c1")
		},
	}

	const timeout = 300 * time.Millisecond
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			f := &fakeTenantService{hang: true}
			client := dial(t, f, token, timeout)

			started := time.Now()
			err := call(context.Background(), client)
			if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), codes.DeadlineExceeded.String()) {
				t.Errorf("expected %v for a deadline, got %v", ErrUnavailable, err)
			}
			if took := time.Since(started); took < timeout/2 || took > 10*time.Second {
				t.Errorf("returned after %s with a timeout of %s", took, timeout)
			}
			if f.reached() != 1 {
				t.Errorf("expected one attempt, tenant-service saw %d", f.reached())
			}
		})
	}

	t.Run("shorter caller deadline", func(t *testing.T) {
		client := dial(t, &fakeTenantService{hang: true}, token, time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		started := time.Now()
		if _, err := client.GetTenantSSOPolicy(ctx, "t"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("expected %v, got %v", ErrUnavailable, err)
		}
		if took := time.Since(started); took > 10*time.Second {
			t.Errorf("expected the caller's own deadline kept, returned after %s", took)
		}
	})

	t.Run("deadline sent", func(t *testing.T) {
		f := &fakeTenantService{context: &v0tenant.SignInContext{}}

		if _, err := dial(t, f, token, callTimeout).GetSignInContext(context.Background(), "t", "", "i"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.deadline <= 0 || f.deadline > callTimeout || f.deadline < callTimeout-2*time.Second {
			t.Errorf("expected about %s left on arrival, got %s", callTimeout, f.deadline)
		}
	})
}
