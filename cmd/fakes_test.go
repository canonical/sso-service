// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	v0tenant "github.com/canonical/identity-platform-api/v0/tenant"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/canonical/sso-service/internal/kratos"
)

// Fakes of what sso-service talks to, for the sign-in flow test
// (flow_integration_test.go): Kratos's and hydra-sso's admin APIs over HTTP,
// tenant-service over gRPC. The customer's identity provider is
// internal/testhelpers/mockidp.

// fakeKratos is Kratos's admin API as sso-service uses it. sso-service
// never writes a link or an account: the tests write them as Kratos would
// (link, register) and count any write sso-service attempts (writes). The
// one thing it removes is a link.
type fakeKratos struct {
	mu         sync.Mutex
	identities map[string]*kratos.Identity
	passwords  map[string]bool
	writes     int
}

func newFakeKratos() *fakeKratos {
	return &fakeKratos{identities: map[string]*kratos.Identity{}, passwords: map[string]bool{}}
}

func (k *fakeKratos) add(email string, password bool) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	id := uuid.NewString()
	k.identities[id] = &kratos.Identity{
		ID: id, Traits: json.RawMessage(`{"email":"` + email + `"}`),
		Credentials: map[string]kratos.Credential{"oidc": {Config: json.RawMessage(`{"providers":[]}`)}},
	}
	if password {
		k.identities[id].Credentials["password"] = kratos.Credential{Config: json.RawMessage(`{"hashed_password":"$2a$h"}`)}
	}
	return id
}

func (k *fakeKratos) byEmail(email string) *kratos.Identity {
	for _, i := range k.identities {
		if strings.EqualFold(i.Email(), email) {
			return i
		}
	}
	return nil
}

func (k *fakeKratos) providers(i *kratos.Identity) []kratos.OIDCProvider {
	return i.OIDCProviders()
}

func (k *fakeKratos) setProviders(i *kratos.Identity, p []kratos.OIDCProvider) {
	raw, _ := json.Marshal(map[string]any{"providers": p})
	i.Credentials["oidc"] = kratos.Credential{Config: raw}
}

// link writes a byo-sso link as Kratos's account linking does.
func (k *fakeKratos) link(id, connectionID, subject string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	i := k.identities[id]
	k.setProviders(i, append(k.providers(i), kratos.OIDCProvider{Provider: "byo-sso", Subject: connectionID + ":" + subject}))
}

// register creates an account through byo-sso, as Kratos's registration
// does: with the link as its only way in.
func (k *fakeKratos) register(email, connectionID, subject string) string {
	id := k.add(email, false)
	k.link(id, connectionID, subject)
	return id
}

func (k *fakeKratos) exists(id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.identities[id] != nil
}

func (k *fakeKratos) writeCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.writes
}

func (k *fakeKratos) find(email string) *kratos.Identity {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.byEmail(email)
}

func (k *fakeKratos) links(id string) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := []string{}
	for _, p := range k.providers(k.identities[id]) {
		out = append(out, p.Provider+":"+p.Subject)
	}
	return out
}

// encodeIdentities answers as Kratos does: JSON with the schema_id and
// schema_url the SDK requires of an identity.
func encodeIdentities(w http.ResponseWriter, v any) {
	raw, _ := json.Marshal(v)
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	add := func(i any) {
		if m, ok := i.(map[string]any); ok {
			m["schema_id"] = "default"
			m["schema_url"] = "http://kratos.test/schemas/default"
		}
	}
	if list, ok := decoded.([]any); ok {
		for _, i := range list {
			add(i)
		}
	} else {
		add(decoded)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(decoded)
}

func (k *fakeKratos) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/admin/identities")
	id, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")

	switch {
	case r.Method == http.MethodGet && id == "":
		identifier := r.URL.Query().Get("credentials_identifier")
		out := []kratos.Identity{}
		for _, i := range k.identities {
			match := strings.EqualFold(i.Email(), identifier)
			for _, p := range k.providers(i) {
				match = match || p.Provider+":"+p.Subject == identifier
			}
			if match {
				out = append(out, kratos.Identity{ID: i.ID, Traits: i.Traits})
			}
		}
		encodeIdentities(w, out)
	case k.identities[id] == nil:
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet:
		encodeIdentities(w, k.identities[id])
	case r.Method == http.MethodDelete && rest == "credentials/oidc":
		identifier := r.URL.Query().Get("identifier")
		kept := []kratos.OIDCProvider{}
		for _, p := range k.providers(k.identities[id]) {
			if p.Provider+":"+p.Subject != identifier {
				kept = append(kept, p)
			}
		}
		k.setProviders(k.identities[id], kept)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut, r.Method == http.MethodPatch, r.Method == http.MethodDelete:
		// sso-service never writes a link or an address, and never removes
		// an account.
		k.writes++
		w.WriteHeader(http.StatusForbidden)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type fakeHydra struct {
	mu       sync.Mutex
	hints    map[string]string
	accepted map[string]string
	rejected map[string]string
	contexts map[string]map[string]any
}

func newFakeHydra() *fakeHydra {
	return &fakeHydra{hints: map[string]string{}, accepted: map[string]string{}, rejected: map[string]string{}, contexts: map[string]map[string]any{}}
}

func (h *fakeHydra) challenge(ticket string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := uuid.NewString()
	h.hints[c] = ticket
	return c
}

func (h *fakeHydra) acceptedFor(challenge string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.accepted[challenge]
}

func (h *fakeHydra) rejectedFor(challenge string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rejected[challenge]
}

func (h *fakeHydra) rejectedAll() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]string{}
	for k, v := range h.rejected {
		out[k] = v
	}
	return out
}

func (h *fakeHydra) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	challenge := r.URL.Query().Get("login_challenge")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/admin/oauth2/auth/requests/login":
		hint, ok := h.hints[challenge]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"challenge": challenge, "oidc_context": map[string]any{"login_hint": hint}, "client": map[string]any{"client_id": "kratos"},
			"request_url": "https://hydra-sso.test/oauth2/auth", "requested_access_token_audience": []string{},
			"requested_scope": []string{"openid", "email"}, "skip": false, "subject": "",
		})
	case "/admin/oauth2/auth/requests/login/accept":
		if body["remember"] != false {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		h.accepted[challenge] = body["subject"].(string)
		h.contexts[challenge], _ = body["context"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"redirect_to": "https://kratos.example/accepted/" + challenge})
	case "/admin/oauth2/auth/requests/login/reject":
		h.rejected[challenge] = body["error_description"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"redirect_to": "https://kratos.example/rejected/" + challenge})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// fakeTenants is tenant-service as sso-service uses it. It holds each
// tenant's SSO policy and checks its rules as tenant-service does: a write
// changes one part of the stored policy, and is refused when the policy as a
// whole would break a rule.
type fakeTenants struct {
	v0tenant.UnimplementedTenantSignInServiceServer
	v0tenant.UnimplementedTenantSSOPolicyServiceServer

	mu       sync.Mutex
	kratos   *fakeKratos
	names    map[string]string
	personal map[string]bool
	members  map[string]map[string]bool // tenant → identity id → a member
	invited  map[string]map[string]bool // tenant → address → a pending invitation
	// policies are the stored policies; a stored one is replaced, never
	// changed, by a write.
	policies map[string]*v0tenant.TenantSSOPolicy
}

func newFakeTenants(k *fakeKratos) *fakeTenants {
	return &fakeTenants{
		kratos: k, names: map[string]string{}, personal: map[string]bool{}, members: map[string]map[string]bool{},
		invited: map[string]map[string]bool{}, policies: map[string]*v0tenant.TenantSSOPolicy{},
	}
}

// refusal is an error as tenant-service answers one its callers branch on: a
// status with the reason.
func refusal(code codes.Code, reason, message string) error {
	st, _ := status.New(code, message).WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "tenant-service"})
	return st.Err()
}

func (f *fakeTenants) tenant(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.NewString()
	f.names[id] = name
	f.members[id] = map[string]bool{}
	f.invited[id] = map[string]bool{}
	return id
}

// invite records a pending invitation (a new address at a REQUIRED tenant).
func (f *fakeTenants) invite(tenant, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invited[tenant][strings.ToLower(email)] = true
}

func (f *fakeTenants) isMember(tenant, identity string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[tenant][identity]
}

func (f *fakeTenants) join(tenant, identity string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[tenant][identity] = true
}

// policy is the tenant's stored policy, nil when it has none.
func (f *fakeTenants) policy(tenant string) *v0tenant.TenantSSOPolicy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policies[tenant]
}

// noPolicy is the policy of a tenant that never wrote one: OFF, nothing bound.
func noPolicy(tenant string) *v0tenant.TenantSSOPolicy {
	return &v0tenant.TenantSSOPolicy{TenantId: tenant, Enforcement: v0tenant.Enforcement_ENFORCEMENT_OFF}
}

// changePolicy applies change to the tenant's stored policy and stores the
// result when the policy as a whole keeps the rules: REQUIRED needs an
// active binding, auto-join needs REQUIRED and domains. Otherwise the write
// is refused and nothing changes. change reports false when it has nothing
// to do; the policy is then answered as it is, with false. A tenant that
// never wrote a policy starts from OFF with nothing bound, and stays OFF
// until its enforcement is written.
func (f *fakeTenants) changePolicy(tenant string, change func(*v0tenant.TenantSSOPolicy) bool) (*v0tenant.TenantSSOPolicy, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.names[tenant]; !ok {
		return nil, false, status.Error(codes.NotFound, "tenant not found")
	}
	if f.personal[tenant] {
		return nil, false, refusal(codes.FailedPrecondition, "PERSONAL_TENANT", "a personal tenant has no sso policy")
	}
	stored := f.policies[tenant]
	if stored == nil {
		stored = noPolicy(tenant)
	}
	next := &v0tenant.TenantSSOPolicy{TenantId: stored.TenantId, Enforcement: stored.Enforcement, AutoJoin: stored.AutoJoin,
		Domains: slices.Clone(stored.Domains), Bindings: slices.Clone(stored.Bindings)}
	if !change(next) {
		return stored, false, nil
	}

	required := next.Enforcement == v0tenant.Enforcement_ENFORCEMENT_REQUIRED
	if next.AutoJoin && (!required || len(next.Domains) == 0) {
		return nil, false, refusal(codes.FailedPrecondition, "AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS", "auto-join needs domains and REQUIRED enforcement")
	}
	if required && !slices.ContainsFunc(next.Bindings, (*v0tenant.SSOBinding).GetActive) {
		return nil, false, refusal(codes.FailedPrecondition, "REQUIRED_NEEDS_ACTIVE_BINDING", "REQUIRED enforcement needs an active binding")
	}
	f.policies[tenant] = next

	return next, true, nil
}

// remove deletes a tenant, as tenant-service's DeleteTenant does: its policy
// and memberships go; the connections it owned at sso-service stay.
func (f *fakeTenants) remove(tenant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.names, tenant)
	delete(f.members, tenant)
	delete(f.policies, tenant)
}

// requireToken is sso-service's client credentials, as tenant-service checks them.
func requireToken(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if !slices.Contains(md.Get("authorization"), "Bearer svc-token") {
		return nil, status.Error(codes.Unauthenticated, "no service token")
	}
	return handler(ctx, req)
}

func (f *fakeTenants) GetSignInContext(_ context.Context, r *v0tenant.GetSignInContextRequest) (*v0tenant.GetSignInContextResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.names[r.TenantId]; !ok {
		return nil, status.Error(codes.NotFound, "no tenant")
	}
	identityID, email := r.IdentityId, r.Email
	f.kratos.mu.Lock()
	if identityID != "" {
		if i := f.kratos.identities[identityID]; i != nil {
			email = i.Email()
		}
	} else if i := f.kratos.byEmail(email); i != nil {
		identityID = i.ID
	}
	f.kratos.mu.Unlock()

	out := &v0tenant.SignInContext{AccountExists: identityID != ""}
	isMember := f.members[r.TenantId][identityID]
	out.Member = isMember
	out.Enforcement = v0tenant.Enforcement_ENFORCEMENT_OFF
	if p := f.policies[r.TenantId]; p != nil {
		_, domain, _ := strings.Cut(strings.ToLower(email), "@")
		inDomains := len(p.Domains) == 0 || slices.Contains(p.Domains, domain)
		for _, b := range p.Bindings {
			if b.Active && inDomains {
				out.ConnectionIds = append(out.ConnectionIds, b.ConnectionId)
			}
		}
		if len(out.ConnectionIds) > 0 {
			out.Enforcement = p.Enforcement
		}
		out.AutoJoinAdmits = !isMember && p.AutoJoin && p.Enforcement == v0tenant.Enforcement_ENFORCEMENT_REQUIRED &&
			len(p.Domains) > 0 && slices.Contains(p.Domains, domain)
	}
	out.InvitationAdmits = !isMember && f.invited[r.TenantId][strings.ToLower(email)]

	return &v0tenant.GetSignInContextResponse{Context: out}, nil
}

func (f *fakeTenants) GetTenantSSOPolicy(_ context.Context, r *v0tenant.GetTenantSSOPolicyRequest) (*v0tenant.GetTenantSSOPolicyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.names[r.TenantId]; !ok {
		return nil, status.Error(codes.NotFound, "no tenant")
	}
	if f.personal[r.TenantId] {
		return nil, refusal(codes.FailedPrecondition, "PERSONAL_TENANT", "a personal tenant has no sso policy")
	}
	if p := f.policies[r.TenantId]; p != nil {
		return &v0tenant.GetTenantSSOPolicyResponse{Policy: p}, nil
	}
	return &v0tenant.GetTenantSSOPolicyResponse{Policy: noPolicy(r.TenantId)}, nil
}

// PutTenantSSOPolicy writes the enforcement, auto-join and bindings; the
// domains stay as stored.
func (f *fakeTenants) PutTenantSSOPolicy(_ context.Context, r *v0tenant.PutTenantSSOPolicyRequest) (*v0tenant.PutTenantSSOPolicyResponse, error) {
	if r.Enforcement != v0tenant.Enforcement_ENFORCEMENT_REQUIRED && r.Enforcement != v0tenant.Enforcement_ENFORCEMENT_OPTIONAL {
		return nil, status.Error(codes.InvalidArgument, "invalid sso policy: enforcement must be OPTIONAL or REQUIRED")
	}
	policy, _, err := f.changePolicy(r.TenantId, func(p *v0tenant.TenantSSOPolicy) bool {
		p.Enforcement, p.AutoJoin, p.Bindings = r.Enforcement, r.AutoJoin, r.Bindings
		return true
	})
	if err != nil {
		return nil, err
	}

	return &v0tenant.PutTenantSSOPolicyResponse{Policy: policy}, nil
}

// SetTenantSSODomains writes the domains; the enforcement, auto-join and
// bindings stay as stored.
func (f *fakeTenants) SetTenantSSODomains(_ context.Context, r *v0tenant.SetTenantSSODomainsRequest) (*v0tenant.SetTenantSSODomainsResponse, error) {
	policy, _, err := f.changePolicy(r.TenantId, func(p *v0tenant.TenantSSOPolicy) bool {
		p.Domains = r.Domains
		return true
	})
	if err != nil {
		return nil, err
	}

	return &v0tenant.SetTenantSSODomainsResponse{Policy: policy}, nil
}

// RemoveTenantSSOBinding removes one binding, if the policy has it; the rest
// stays as stored.
func (f *fakeTenants) RemoveTenantSSOBinding(_ context.Context, r *v0tenant.RemoveTenantSSOBindingRequest) (*v0tenant.RemoveTenantSSOBindingResponse, error) {
	_, _, err := f.changePolicy(r.TenantId, func(p *v0tenant.TenantSSOPolicy) bool {
		bound := len(p.Bindings)
		p.Bindings = slices.DeleteFunc(p.Bindings, func(b *v0tenant.SSOBinding) bool { return strings.EqualFold(b.ConnectionId, r.ConnectionId) })
		return len(p.Bindings) != bound
	})
	if err != nil {
		return nil, err
	}

	return &v0tenant.RemoveTenantSSOBindingResponse{}, nil
}

// formFrom reads the hidden inputs of the mock IdP's login form out of the
// authorization URL it was served for.
func formFrom(authorizeURL, login string) url.Values {
	u, _ := url.Parse(authorizeURL)
	form := u.Query()
	form.Set("login", login)
	return form
}
