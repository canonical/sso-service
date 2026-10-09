// Copyright 2024 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	UserAgentKey     = "useragent"
	SourceIpKey      = "source_ip"
	HostnameKey      = "hostname"
	ProtocolKey      = "protocol"
	PortKey          = "port"
	RequestUriKey    = "request_uri"
	RequestMethodKey = "request_method"
)

// brain-picked from DefaultLogFormatter https://raw.githubusercontent.com/go-chi/chi/v5.0.8/middleware/logger.go

// LogFormatter is a simple logger that implements a middleware.LogFormatter.
type LogFormatter struct {
	Logger LoggerInterface
}

// NewLogEntry creates a new LogEntry for the request.
func (l *LogFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	entry := new(LogEntry)

	entry.LogFormatter = l
	entry.request = r
	entry.buf = new(bytes.Buffer)

	reqID := middleware.GetReqID(r.Context())
	if reqID != "" {
		fmt.Fprintf(entry.buf, "[%s] ", reqID)
	}

	fmt.Fprintf(entry.buf, "%s ", r.Method)

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	// The path only: a query holds authorization codes and addresses.
	fmt.Fprintf(entry.buf, "%s://%s%s %s ", scheme, r.Host, r.URL.Path, r.Proto)
	fmt.Fprintf(entry.buf, "from %s ", r.RemoteAddr)

	return entry
}

type LogEntry struct {
	*LogFormatter
	request *http.Request
	buf     *bytes.Buffer
}

func (l *LogEntry) Write(status, bytes int, header http.Header, elapsed time.Duration, extra interface{}) {

	fmt.Fprintf(l.buf, "%03d %dB in %s", status, bytes, elapsed)

	l.Logger.Debug(l.buf.String())
}

func (l *LogEntry) Panic(v interface{}, stack []byte) {
	return
}

func NewLogFormatter(logger LoggerInterface) *LogFormatter {
	l := new(LogFormatter)

	l.Logger = logger

	return l
}
