// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package monitoring

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/canonical/sso-service/internal/logging"
	chi "github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	// IDPathRegex regexp used to swap the {id*} parameters in the path with simply id
	// supports alphabetic characters and underscores, no dashes
	IDPathRegex string = "{[a-zA-Z_]*}"

	// unmatchedRoute labels every request no route matched.
	unmatchedRoute = "unmatched"
)

// Middleware is the monitoring middleware object implementing Prometheus monitoring
type Middleware struct {
	service string
	regex   *regexp.Regexp

	monitor MonitorInterface
	logger  logging.LoggerInterface
}

func (mdw *Middleware) ResponseTime() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
				startTime := time.Now()

				next.ServeHTTP(ww, r)

				tags := map[string]string{
					"route":  mdw.route(r),
					"status": strconv.Itoa(ww.Status()),
				}

				mdw.monitor.SetResponseTimeMetric(tags, time.Since(startTime).Seconds())
			},
		)
	}
}

// route is the method and the pattern of the route that served the request,
// never its path: a label per path is a series per path.
func (mdw *Middleware) route(r *http.Request) string {
	pattern := chi.RouteContext(r.Context()).RoutePattern()
	if pattern == "" {
		return unmatchedRoute
	}

	return fmt.Sprintf("%s%s", r.Method, mdw.regex.ReplaceAll([]byte(pattern), []byte("id")))
}

// NewMiddleware returns a Middleware based on the type of monitor
func NewMiddleware(monitor MonitorInterface, logger logging.LoggerInterface) *Middleware {
	mdw := new(Middleware)

	mdw.monitor = monitor

	mdw.service = monitor.GetService()
	mdw.logger = logger
	mdw.regex = regexp.MustCompile(IDPathRegex)

	return mdw
}
