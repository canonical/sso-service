// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

// Package mockidp is a minimal OpenID Connect provider for tests, whose
// users the tests configure: an address reported unconfirmed or not at all,
// an address that changed, a session that answers without its form.
package mockidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// SessionCookie holds the browser's login at the mock IdP.
const SessionCookie = "mockidp_session"

const (
	codeLifetime  = 2 * time.Minute
	tokenLifetime = 5 * time.Minute
)

// Config is the provider's identity and its one client.
type Config struct {
	// Issuer is the issuer URL, as sso-service and the browser both reach it.
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURIPrefix is what the client's redirect URIs start with:
	// sso-service's callback, which ends in each connection's id.
	RedirectURIPrefix string
}

// User is what the IdP asserts for a login name typed on its form. A login
// with no configured user is asserted as itself: sub "mock|<login>", email
// <login>, email_verified true.
type User struct {
	Login   string `json:"login"`
	Subject string `json:"sub"`
	Email   string `json:"email"`
	// EmailVerified nil leaves the claim out of the id_token.
	EmailVerified *bool `json:"email_verified"`
}

type grant struct {
	user        User
	redirectURI string
	nonce       string
	challenge   string
	authTime    time.Time
	expires     time.Time
}

// session is a browser's login at the mock IdP.
type session struct {
	login    string
	authTime time.Time
}

// Server is the mock IdP.
type Server struct {
	cfg    Config
	signer jose.Signer
	jwks   jose.JSONWebKeySet

	mu       sync.Mutex
	users    map[string]User
	codes    map[string]grant
	sessions map[string]session
}

// New generates the signing key; its key id is new at every start too.
func New(cfg Config) (*Server, error) {
	cfg.Issuer = strings.TrimSuffix(cfg.Issuer, "/")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	keyID := randomToken()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: keyID}}, nil)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:      cfg,
		signer:   signer,
		jwks:     jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"}}},
		users:    map[string]User{},
		codes:    map[string]grant{},
		sessions: map[string]session{},
	}, nil
}

// Handler serves the provider and its admin API (/admin/users/{login},
// DELETE /admin/state).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /jwks", s.keys)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /authorize", s.login)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("PUT /admin/users/{login}", s.putUser)
	mux.HandleFunc("DELETE /admin/users/{login}", s.deleteUser)
	mux.HandleFunc("DELETE /admin/state", s.reset)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.cfg.Issuer,
		"authorization_endpoint":                s.cfg.Issuer + "/authorize",
		"token_endpoint":                        s.cfg.Issuer + "/token",
		"jwks_uri":                              s.cfg.Issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"scopes_supported":                      []string{"openid", "email", "profile"},
	})
}

func (s *Server) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.jwks)
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><title>Mock company sign-in</title></head>
<body><h1>Mock company sign-in</h1>
<form method="post" action="authorize">
{{range $k, $v := .}}{{if ne $k "login"}}<input type="hidden" name="{{$k}}" value="{{index $v 0}}">{{end}}
{{end}}<label for="login">Login</label> <input id="login" name="login" type="text">
<button type="submit">Sign in</button>
</form></body></html>`))

// authorize answers from the session when the browser holds one, otherwise
// shows the login form. prompt=login or max_age=0 always shows the form, so
// auth_time is fresh (re-authentication).
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !s.validRequest(w, q) {
		return
	}
	fresh := q.Get("prompt") == "login" || q.Get("max_age") == "0"
	if c, err := r.Cookie(SessionCookie); err == nil && !fresh {
		s.mu.Lock()
		sess, ok := s.sessions[c.Value]
		s.mu.Unlock()
		if ok {
			s.issueCode(w, r, q, sess)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginPage.Execute(w, q)
}

// login takes the form: every login succeeds, and starts a session.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	q := r.PostForm
	if !s.validRequest(w, q) {
		return
	}
	login := strings.TrimSpace(q.Get("login"))
	if login == "" {
		http.Error(w, "login required", http.StatusBadRequest)
		return
	}
	id := randomToken()
	sess := session{login: login, authTime: time.Now()}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	s.issueCode(w, r, q, sess)
}

func (s *Server) validRequest(w http.ResponseWriter, q url.Values) bool {
	switch {
	case q.Get("client_id") != s.cfg.ClientID:
		http.Error(w, "unknown client", http.StatusBadRequest)
	case q.Get("response_type") != "code":
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
	case s.cfg.RedirectURIPrefix == "" || !strings.HasPrefix(q.Get("redirect_uri"), s.cfg.RedirectURIPrefix):
		http.Error(w, "unknown redirect_uri", http.StatusBadRequest)
	case q.Get("code_challenge") != "" && q.Get("code_challenge_method") != "S256":
		http.Error(w, "unsupported code_challenge_method", http.StatusBadRequest)
	default:
		return true
	}
	return false
}

func (s *Server) issueCode(w http.ResponseWriter, r *http.Request, q url.Values, sess session) {
	to, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}

	code := randomToken()
	now := time.Now()
	s.mu.Lock()
	for c, g := range s.codes {
		if now.After(g.expires) {
			delete(s.codes, c)
		}
	}
	s.codes[code] = grant{
		user:        s.userLocked(sess.login),
		redirectURI: q.Get("redirect_uri"),
		nonce:       q.Get("nonce"),
		challenge:   q.Get("code_challenge"),
		authTime:    sess.authTime,
		expires:     now.Add(codeLifetime),
	}
	s.mu.Unlock()

	back := to.Query()
	back.Set("code", code)
	if state := q.Get("state"); state != "" {
		back.Set("state", state)
	}
	to.RawQuery = back.Encode()
	http.Redirect(w, r, to.String(), http.StatusFound)
}

func (s *Server) userLocked(login string) User {
	if u, ok := s.users[login]; ok {
		return u
	}
	verified := true
	return User{Login: login, Subject: "mock|" + login, Email: login, EmailVerified: &verified}
}

// token exchanges a code, answering the OAuth errors the Bridge's credential
// probe reads: invalid_client for wrong credentials, invalid_grant for a
// code it never issued.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		// RFC 6749 §2.3.1: form-encoded before Basic encoding (x/oauth2 does).
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != s.cfg.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.ClientSecret)) != 1 {
		if basic {
			w.Header().Set("WWW-Authenticate", `Basic realm="mock-idp"`)
		}
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}

	s.mu.Lock()
	g, found := s.codes[r.PostForm.Get("code")]
	delete(s.codes, r.PostForm.Get("code"))
	s.mu.Unlock()
	if !found || time.Now().After(g.expires) || g.redirectURI != r.PostForm.Get("redirect_uri") || !pkceMatches(g.challenge, r.PostForm.Get("code_verifier")) {
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}

	now := time.Now()
	claims := map[string]any{
		"iss":       s.cfg.Issuer,
		"sub":       g.user.Subject,
		"aud":       s.cfg.ClientID,
		"iat":       now.Unix(),
		"exp":       now.Add(tokenLifetime).Unix(),
		"email":     g.user.Email,
		"auth_time": g.authTime.Unix(),
	}
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	if g.user.EmailVerified != nil {
		claims["email_verified"] = *g.user.EmailVerified
	}
	payload, _ := json.Marshal(claims)
	signed, err := s.signer.Sign(payload)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	idToken, err := signed.CompactSerialize()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": randomToken(),
		"token_type":   "Bearer",
		"expires_in":   int(tokenLifetime.Seconds()),
		"id_token":     idToken,
	})
}

func (s *Server) putUser(w http.ResponseWriter, r *http.Request) {
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		http.Error(w, "bad user", http.StatusBadRequest)
		return
	}
	u.Login = r.PathValue("login")
	if u.Subject == "" || u.Email == "" {
		http.Error(w, "sub and email are required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.users[u.Login] = u
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	delete(s.users, r.PathValue("login"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// reset forgets every user, code and session.
func (s *Server) reset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.users = map[string]User{}
	s.codes = map[string]grant{}
	s.sessions = map[string]session{}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func pkceMatches(challenge, verifier string) bool {
	if challenge == "" {
		return verifier == ""
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
