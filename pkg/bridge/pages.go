// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

import (
	"html/template"
	"net/http"
)

type Page struct {
	Title   string
	Message string
}

func testSucceededPage(label string, authTimeReturned bool) *Page {
	message := "The test sign-in through " + label + " succeeded. You can close this window."
	if !authTimeReturned {
		message += " The identity provider returned no auth_time: apps that ask for a fresh sign-in will fail through this connection."
	}

	return &Page{Title: "Test sign-in succeeded", Message: message}
}

func messagePage(title, message string) *Page {
	return &Page{Title: title, Message: message}
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5}</style>
</head><body>
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
</body></html>`))

func renderPage(w http.ResponseWriter, status int, page *Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	_ = pageTemplate.Execute(w, page)
}
