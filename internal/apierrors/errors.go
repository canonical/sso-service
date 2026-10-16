// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

// Package apierrors builds gRPC status errors that carry a machine-readable
// reason.
package apierrors

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const domain = "sso-service"

// Reasons are part of the API.
const (
	ConnectionNotTested             = "CONNECTION_NOT_TESTED"
	RequiredNeedsActiveBinding      = "REQUIRED_NEEDS_ACTIVE_BINDING"
	AutoJoinNeedsRequiredAndDomains = "AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS"
	ConnectionLimit                 = "CONNECTION_LIMIT"
	ConnectionNotFound              = "CONNECTION_NOT_FOUND"
	PersonalTenant                  = "PERSONAL_TENANT"
	NotApplicable                   = "NOT_APPLICABLE"
	LastCredential                  = "LAST_CREDENTIAL"
	IdPCheckFailed                  = "IDP_CHECK_FAILED"
)

// New returns a status error with the reason in an ErrorInfo detail, and at
// the start of the message too: the HTTP gateway drops the details.
func New(code codes.Code, reason, message string) error {
	st := status.New(code, reason+": "+message)
	if detailed, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: domain}); err == nil {
		st = detailed
	}

	return st.Err()
}

func Reason(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}

	return ""
}
