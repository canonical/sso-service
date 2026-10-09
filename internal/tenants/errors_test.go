// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tenants

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func withReason(t *testing.T, code codes.Code, reason string) error {
	t.Helper()
	st, err := status.New(code, reason+": refused").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "tenant-service"})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestFromStatus(t *testing.T) {
	testCases := []struct {
		name        string
		err         error
		expectedErr error
		// expectedText is tenant-service's own text, where it is passed on.
		expectedText string
	}{
		{
			name:        "personal tenant",
			err:         withReason(t, codes.FailedPrecondition, "PERSONAL_TENANT"),
			expectedErr: ErrPersonalTenant,
		},
		{
			name:        "required needs active binding",
			err:         withReason(t, codes.FailedPrecondition, "REQUIRED_NEEDS_ACTIVE_BINDING"),
			expectedErr: ErrRequiredNeedsActiveBinding,
		},
		{
			name:        "auto-join needs required and domains",
			err:         withReason(t, codes.FailedPrecondition, "AUTO_JOIN_NEEDS_REQUIRED_AND_DOMAINS"),
			expectedErr: ErrAutoJoinNeedsRequiredAndDomains,
		},
		{
			name:        "not found",
			err:         status.Error(codes.NotFound, "tenant 123 not found in table tenants"),
			expectedErr: ErrTenantNotFound,
		},
		{
			name:        "aborted",
			err:         status.Error(codes.Aborted, "lock timeout on table tenants"),
			expectedErr: ErrBusy,
		},
		{
			name:         "invalid argument",
			err:          status.Error(codes.InvalidArgument, "invalid sso policy: a connection is bound twice"),
			expectedErr:  ErrInvalid,
			expectedText: "invalid sso policy: a connection is bound twice",
		},
		{
			name:         "failed precondition",
			err:          status.Error(codes.FailedPrecondition, "a connection is bound by another tenant"),
			expectedErr:  ErrRefused,
			expectedText: "a connection is bound by another tenant",
		},
		{
			name:         "another reason",
			err:          withReason(t, codes.FailedPrecondition, "SOMETHING_ELSE"),
			expectedErr:  ErrRefused,
			expectedText: "SOMETHING_ELSE: refused",
		},
		{
			name:        "unavailable",
			err:         status.Error(codes.Unavailable, "dial tcp 10.1.2.3:50051"),
			expectedErr: ErrUnavailable,
		},
		{
			name:        "deadline exceeded",
			err:         status.Error(codes.DeadlineExceeded, "context deadline exceeded"),
			expectedErr: ErrUnavailable,
		},
		{
			name:        "internal",
			err:         status.Error(codes.Internal, "pq: relation does not exist"),
			expectedErr: ErrUnavailable,
		},
		{
			name:        "unauthenticated",
			err:         status.Error(codes.Unauthenticated, "invalid token"),
			expectedErr: ErrUnavailable,
		},
		{
			name:        "not a status",
			err:         errors.New("failed to get a service token: hydra down"),
			expectedErr: ErrUnavailable,
		},
	}

	sentinels := []error{
		ErrPersonalTenant, ErrRequiredNeedsActiveBinding, ErrAutoJoinNeedsRequiredAndDomains,
		ErrTenantNotFound, ErrBusy, ErrInvalid, ErrRefused, ErrUnavailable,
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := fromStatus(tc.err)

			for _, sentinel := range sentinels {
				if got, want := errors.Is(err, sentinel), sentinel == tc.expectedErr; got != want {
					t.Errorf("expected errors.Is(%v) to be %v for %v", sentinel, want, err)
				}
			}
			if tc.expectedText != "" && !strings.HasSuffix(err.Error(), ": "+tc.expectedText) {
				t.Errorf("expected tenant-service's text %q passed on, got %q", tc.expectedText, err.Error())
			}
			// What is no refusal keeps its cause, for the log.
			if tc.expectedErr == ErrUnavailable && (!strings.HasSuffix(err.Error(), ": "+tc.err.Error()) || status.Code(err) != codes.Unknown) {
				t.Errorf("expected the cause as text and no status of tenant-service's, got %v", err)
			}
		})
	}
}
