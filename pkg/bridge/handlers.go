// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	chi "github.com/go-chi/chi/v5"

	"github.com/canonical/sso-service/internal/limits"
	"github.com/canonical/sso-service/internal/logging"
	"github.com/canonical/sso-service/internal/tracing"
	"github.com/canonical/sso-service/internal/types"
)

// The __Host- prefix makes browsers refuse the cookie unless it is host-only,
// Secure and Path=/.
const (
	bindingCookiePrefix = "__Host-sso_bind_"
	receiptCookiePrefix = "__Host-sso_receipt_"
)

type API struct {
	service ServiceInterface

	tracer tracing.TracingInterface
	logger logging.LoggerInterface
}

func NewAPI(service ServiceInterface, tracer tracing.TracingInterface, logger logging.LoggerInterface) *API {
	return &API{
		service: service,
		tracer:  tracer,
		logger:  logger,
	}
}

func (a *API) RegisterEndpoints(mux *chi.Mux) {
	mux.Get("/login", a.login)
	mux.Get(types.CallbackPath+"/{connection_id}", a.callback)
	mux.Get("/consent", a.consent)
	mux.Get("/error", a.error)
}

// bindingCookieName is one name per state, so two sign-ins in one browser do
// not overwrite each other's cookie.
func bindingCookieName(state string) string {
	return bindingCookiePrefix + nameSuffix(state)
}

// receiptCookieName is one name per ticket: the login UI reads the cookie of
// the ticket it holds.
func receiptCookieName(ticket string) string {
	return receiptCookiePrefix + nameSuffix(ticket)
}

func nameSuffix(value string) string {
	sum := sha256.Sum256([]byte(value))

	return hex.EncodeToString(sum[:])[:16]
}

func bindingCookie(state, value string, maxAge int) *http.Cookie {
	return hostCookie(bindingCookieName(state), value, maxAge)
}

func hostCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func bindingFrom(r *http.Request, state string) string {
	if state == "" {
		return ""
	}
	if cookie, err := r.Cookie(bindingCookieName(state)); err == nil {
		return cookie.Value
	}

	return ""
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ctx, span := a.tracer.Start(r.Context(), "bridge.API.login")
	defer span.End()

	challenge := r.URL.Query().Get("login_challenge")
	if challenge == "" {
		renderPage(w, http.StatusBadRequest, messagePage("Sign-in failed", "This sign-in request is incomplete."))

		return
	}

	target, binding, err := a.service.StartLogin(ctx, challenge)
	if err != nil {
		a.fail(w, err)

		return
	}
	if binding != nil {
		http.SetCookie(w, bindingCookie(binding.State, binding.Value, int(limits.AttemptTTL.Seconds())))
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *API) callback(w http.ResponseWriter, r *http.Request) {
	ctx, span := a.tracer.Start(r.Context(), "bridge.API.callback")
	defer span.End()

	query := r.URL.Query()
	state := query.Get("state")
	result, err := a.service.Callback(ctx, &Callback{
		ConnectionID:     strings.ToLower(chi.URLParam(r, "connection_id")),
		State:            state,
		Code:             query.Get("code"),
		Error:            query.Get("error"),
		ErrorDescription: query.Get("error_description"),
		Binding:          bindingFrom(r, state),
	})
	if err != nil {
		a.fail(w, err)

		return
	}
	a.respond(w, r, state, result)
}

func (a *API) consent(w http.ResponseWriter, r *http.Request) {
	ctx, span := a.tracer.Start(r.Context(), "bridge.API.consent")
	defer span.End()

	challenge := r.URL.Query().Get("consent_challenge")
	if challenge == "" {
		renderPage(w, http.StatusBadRequest, messagePage("Sign-in failed", "This consent request is incomplete."))

		return
	}
	target, err := a.service.Consent(ctx, challenge)
	if err != nil {
		a.fail(w, err)

		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// error is where hydra-sso sends the browser with a failure it could not
// report to its client.
func (a *API) error(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	a.logger.Warnw("hydra-sso reported an error it could not send to its client",
		"error", bounded(query.Get("error")),
		"error_description", bounded(query.Get("error_description")),
		"error_hint", bounded(query.Get("error_hint")),
	)
	renderPage(w, http.StatusBadRequest, messagePage("Sign-in failed", "Company sign-in could not be completed. Please start again."))
}

func (a *API) respond(w http.ResponseWriter, r *http.Request, state string, result *Result) {
	if state != "" {
		http.SetCookie(w, bindingCookie(state, "", -1))
	}
	if receipt := result.Receipt; receipt != nil {
		http.SetCookie(w, hostCookie(receiptCookieName(receipt.Ticket), receipt.Value, receipt.MaxAge))
	}
	if result.Page != nil {
		renderPage(w, http.StatusOK, result.Page)

		return
	}
	http.Redirect(w, r, result.Redirect, http.StatusSeeOther)
}

func (a *API) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBindingMissing), errors.Is(err, ErrBindingMismatch):
		renderPage(w, http.StatusBadRequest, messagePage("Sign-in failed", err.Error()+". Please start again in this browser."))
	case errors.Is(err, ErrUnknownState), errors.Is(err, ErrUnknownLogin):
		renderPage(w, http.StatusBadRequest, messagePage("Sign-in failed", "This sign-in is unknown, has expired or was already used. Please start again."))
	default:
		a.logger.Errorw("company sign-in failed", "error", err)
		renderPage(w, http.StatusInternalServerError, messagePage("Sign-in failed", "Company sign-in is unavailable right now. Please try again later."))
	}
}
