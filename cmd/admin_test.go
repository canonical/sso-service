// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"reflect"
	"testing"

	tenantv0 "github.com/canonical/identity-platform-api/v0/tenant"
)

func TestParseBindings(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    []*tenantv0.SSOBinding
		wantErr bool
	}{
		{name: "active unless said otherwise", values: []string{"a", "b:active", "c:inactive"}, want: []*tenantv0.SSOBinding{
			{ConnectionId: "a", Active: true}, {ConnectionId: "b", Active: true}, {ConnectionId: "c", Active: false},
		}},
		{name: "none removes every binding", want: []*tenantv0.SSOBinding{}},
		{name: "unknown state", values: []string{"a:off"}, wantErr: true},
		{name: "no connection", values: []string{":inactive"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBindings(tt.values)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseBindings() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(activeByConnection(got), activeByConnection(tt.want)) {
				t.Errorf("parseBindings() = %v, want %v", got, tt.want)
			}
		})
	}
}

func activeByConnection(bindings []*tenantv0.SSOBinding) map[string]bool {
	out := map[string]bool{}
	for _, b := range bindings {
		out[b.GetConnectionId()] = b.GetActive()
	}
	return out
}

func TestConnectionUpdate(t *testing.T) {
	tests := []struct {
		name          string
		label, secret string
		want          []string
	}{
		{name: "label only", label: "Okta", want: []string{"label"}},
		{name: "secret only", secret: "s3cret", want: []string{"client_secret"}},
		{name: "both", label: "Okta", secret: "s3cret", want: []string{"label", "client_secret"}},
		{name: "nothing given"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			update, mask := connectionUpdate(tt.label, tt.secret)
			if !reflect.DeepEqual(mask.GetPaths(), tt.want) {
				t.Errorf("mask = %v, want %v", mask.GetPaths(), tt.want)
			}
			if update.GetLabel() != tt.label || update.GetClientSecret() != tt.secret {
				t.Errorf("update = %v", update)
			}
		})
	}
}
