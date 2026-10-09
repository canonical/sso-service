// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package bridge

// Reasons a company sign-in ends for, as the callback outcomes counter labels
// them.
const (
	ReasonLinkedExisting = "linked_existing"
	ReasonAccountLinking = "account_linking"
	ReasonRegistration   = "registration"

	ReasonIdPRefused         = "idp_refused"
	ReasonInvalidToken       = "invalid_token"
	ReasonReauthentication   = "reauthentication_not_done"
	ReasonAddressMismatch    = "address_mismatch"
	ReasonAddressUnconfirmed = "address_unconfirmed"
	ReasonNotAMember         = "not_a_member"
	ReasonAlreadyLinked      = "already_linked"
	ReasonUnavailable        = "unavailable"
	ReasonExpired            = "expired"
)

// messages are what the user reads: never text an identity provider
// controls.
var messages = map[string]string{
	ReasonIdPRefused:         "Your tenant's sign-in refused the request.",
	ReasonInvalidToken:       "Your tenant's sign-in returned an answer that could not be accepted.",
	ReasonReauthentication:   "Your tenant's sign-in did not ask you to sign in again, as this app requires.",
	ReasonAddressMismatch:    "The account you used at your tenant's sign-in does not match the email address you entered.",
	ReasonAddressUnconfirmed: "Your tenant's sign-in says your email address is not confirmed.",
	ReasonNotAMember:         "This account cannot sign in to this tenant through its company sign-in.",
	ReasonAlreadyLinked:      "This account at your tenant's sign-in is already linked to another account.",
	ReasonUnavailable:        "Company sign-in is unavailable right now. Please try again later.",
	ReasonExpired:            "This sign-in has expired. Please start again.",
}

func Message(reason string) string {
	if m, ok := messages[reason]; ok {
		return m
	}

	return messages[ReasonUnavailable]
}
