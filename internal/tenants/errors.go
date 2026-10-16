// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tenants

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/canonical/sso-service/internal/apierrors"
)

// What tenant-service refuses a policy for.
var (
	ErrPersonalTenant                  = errors.New("a personal tenant has no SSO policy and no connections")
	ErrRequiredNeedsActiveBinding      = errors.New("a tenant that requires company sign-in needs an active binding")
	ErrAutoJoinNeedsRequiredAndDomains = errors.New("auto-join needs domains and REQUIRED enforcement")
)

var (
	ErrTenantNotFound = errors.New("no such tenant")
	ErrBusy           = errors.New("another request is changing this tenant's policy; try again")
	// ErrInvalid and ErrRefused carry tenant-service's own words.
	ErrInvalid = errors.New("tenant-service found the request invalid")
	ErrRefused = errors.New("tenant-service refused the request")
	// ErrUnavailable is any answer that is no refusal. It carries the cause
	// as text only, so tenant-service's status never passes for this
	// service's.
	ErrUnavailable = errors.New("tenant-service is unavailable")
)

var reasons = map[string]error{
	apierrors.PersonalTenant:                  ErrPersonalTenant,
	apierrors.RequiredNeedsActiveBinding:      ErrRequiredNeedsActiveBinding,
	apierrors.AutoJoinNeedsRequiredAndDomains: ErrAutoJoinNeedsRequiredAndDomains,
}

// fromStatus names tenant-service's answer: by its reason when it gives one
// of ours, by its code otherwise.
func fromStatus(err error) error {
	if refusal, ok := reasons[apierrors.Reason(err)]; ok {
		return refusal
	}

	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.NotFound:
		return ErrTenantNotFound
	case codes.Aborted:
		return ErrBusy
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %s", ErrInvalid, st.Message())
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", ErrRefused, st.Message())
	default:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}
