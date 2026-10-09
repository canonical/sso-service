// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package apierrors

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNew(t *testing.T) {
	err := New(codes.FailedPrecondition, ConnectionNotTested, "only a tested connection can be active")

	st := status.Convert(err)
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("expected code %v, got %v", codes.FailedPrecondition, st.Code())
	}
	// The HTTP gateway drops the details: the message carries the reason too.
	if want := "CONNECTION_NOT_TESTED: only a tested connection can be active"; st.Message() != want {
		t.Errorf("expected message %q, got %q", want, st.Message())
	}
	if len(st.Details()) != 1 {
		t.Fatalf("expected one detail, got %v", st.Details())
	}
	info, ok := st.Details()[0].(*errdetails.ErrorInfo)
	if !ok || info.GetReason() != ConnectionNotTested || info.GetDomain() != domain {
		t.Errorf("expected an ErrorInfo with the reason and the domain, got %v", st.Details()[0])
	}
}

func TestReason(t *testing.T) {
	withOtherDetail, err := status.New(codes.NotFound, "no").WithDetails(&errdetails.RequestInfo{RequestId: "r"})
	if err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		name           string
		err            error
		expectedReason string
	}{
		{
			name:           "reason",
			err:            New(codes.NotFound, ConnectionNotFound, "no such connection"),
			expectedReason: ConnectionNotFound,
		},
		{
			name: "wrapped",
			err:  fmt.Errorf("wrapped: %w", New(codes.NotFound, ConnectionNotFound, "no such connection")),
			// status.FromError takes the code and the details of a wrapped status.
			expectedReason: ConnectionNotFound,
		},
		{name: "status without details", err: status.Error(codes.NotFound, "no")},
		{name: "another detail", err: withOtherDetail.Err()},
		{name: "not a status", err: errors.New("boom")},
		{name: "nil"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Reason(tc.err); got != tc.expectedReason {
				t.Errorf("expected reason %q, got %q", tc.expectedReason, got)
			}
		})
	}
}
