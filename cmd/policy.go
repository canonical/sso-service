// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"fmt"
	"strings"

	v0 "github.com/canonical/identity-platform-api/v0/sso"
	tenantv0 "github.com/canonical/identity-platform-api/v0/tenant"
	"github.com/spf13/cobra"
)

var (
	policyEnforcement string
	policyBindings    []string
	policyAutoJoin    bool
)

var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Manage tenants' SSO policies",
}

var getPolicyCmd = &cobra.Command{
	Use:   "get [tenant-id]",
	Short: "Get a tenant's SSO policy",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, tenantAdmin, _, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := tenantAdmin.GetTenantSSOPolicy(authenticatedContext(cmd.Context()), &v0.GetTenantSSOPolicyRequest{
			TenantId: args[0],
		})
		if err != nil {
			return fmt.Errorf("failed to get the policy: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

var setPolicyCmd = &cobra.Command{
	Use:   "set [tenant-id]",
	Short: "Replace a tenant's SSO policy: its enforcement, bindings and auto-join",
	Long: "Replace a tenant's SSO policy. A binding is a connection id, followed by \":inactive\" for a\n" +
		"connection the tenant keeps but does not offer; a binding left out is removed.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		enforcement, err := parseEnforcement(policyEnforcement)
		if err != nil {
			return err
		}
		bindings, err := parseBindings(policyBindings)
		if err != nil {
			return err
		}

		closeConn, tenantAdmin, _, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := tenantAdmin.PutTenantSSOPolicy(authenticatedContext(cmd.Context()), &v0.PutTenantSSOPolicyRequest{
			TenantId:    args[0],
			Enforcement: enforcement,
			Bindings:    bindings,
			AutoJoin:    policyAutoJoin,
		})
		if err != nil {
			return fmt.Errorf("failed to set the policy: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

var domainsCmd = &cobra.Command{
	Use:   "domains",
	Short: "Manage the email domains of tenants",
}

var getDomainsCmd = &cobra.Command{
	Use:   "get [tenant-id]",
	Short: "Get a tenant's email domains",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, _, platformAdmin, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := platformAdmin.GetTenantDomains(authenticatedContext(cmd.Context()), &v0.GetTenantDomainsRequest{
			TenantId: args[0],
		})
		if err != nil {
			return fmt.Errorf("failed to get the domains: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

var setDomainsCmd = &cobra.Command{
	Use:   "set [tenant-id] [domain...]",
	Short: "Replace a tenant's email domains; none given removes them all",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, _, platformAdmin, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := platformAdmin.SetTenantDomains(authenticatedContext(cmd.Context()), &v0.SetTenantDomainsRequest{
			TenantId: args[0],
			Domains:  args[1:],
		})
		if err != nil {
			return fmt.Errorf("failed to set the domains: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

func parseEnforcement(s string) (tenantv0.Enforcement, error) {
	switch strings.ToLower(s) {
	case "optional":
		return tenantv0.Enforcement_ENFORCEMENT_OPTIONAL, nil
	case "required":
		return tenantv0.Enforcement_ENFORCEMENT_REQUIRED, nil
	}

	return tenantv0.Enforcement_ENFORCEMENT_UNSPECIFIED, fmt.Errorf("--enforcement must be optional or required")
}

func parseBindings(values []string) ([]*tenantv0.SSOBinding, error) {
	bindings := make([]*tenantv0.SSOBinding, 0, len(values))
	for _, value := range values {
		id, state, found := strings.Cut(value, ":")
		if id == "" || found && state != "active" && state != "inactive" {
			return nil, fmt.Errorf("invalid binding %q: want <connection-id>[:active|:inactive]", value)
		}

		bindings = append(bindings, &tenantv0.SSOBinding{ConnectionId: id, Active: state != "inactive"})
	}

	return bindings, nil
}

func init() {
	rootCmd.AddCommand(policyCmd)
	policyCmd.AddCommand(getPolicyCmd)
	policyCmd.AddCommand(setPolicyCmd)

	rootCmd.AddCommand(domainsCmd)
	domainsCmd.AddCommand(getDomainsCmd)
	domainsCmd.AddCommand(setDomainsCmd)

	setPolicyCmd.Flags().StringVar(&policyEnforcement, "enforcement", "", "optional or required")
	setPolicyCmd.Flags().StringArrayVar(&policyBindings, "binding", nil, "A bound connection: <connection-id>[:inactive] (repeatable)")
	setPolicyCmd.Flags().BoolVar(&policyAutoJoin, "auto-join", false, "Admit addresses in the tenant's domains at their first sign-in")
	_ = setPolicyCmd.MarkFlagRequired("enforcement")
}
