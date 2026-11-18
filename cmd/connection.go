// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"fmt"
	"os"

	v0 "github.com/canonical/identity-platform-api/v0/sso"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// clientSecretEnv carries a client secret to a command without putting it in its arguments.
const clientSecretEnv = "CLIENT_SECRET"

var (
	connectionLabel        string
	connectionIssuer       string
	connectionClientID     string
	connectionClientSecret string
	connectionPageSize     int32
	connectionPageToken    string
)

var connectionCmd = &cobra.Command{
	Use:   "connection",
	Short: "Manage tenants' connections to their identity providers",
}

var listConnectionsCmd = &cobra.Command{
	Use:   "list [tenant-id]",
	Short: "List a tenant's connections, or every tenant's when no tenant is given",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, tenantAdmin, platformAdmin, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		ctx := authenticatedContext(cmd.Context())
		if len(args) == 0 {
			resp, err := platformAdmin.ListAllConnections(ctx, &v0.ListAllConnectionsRequest{
				PageSize:  connectionPageSize,
				PageToken: connectionPageToken,
			})
			if err != nil {
				return fmt.Errorf("failed to list connections: %w", err)
			}

			return printMessage(cmd, resp)
		}

		resp, err := tenantAdmin.ListConnections(ctx, &v0.ListConnectionsRequest{
			TenantId:  args[0],
			PageSize:  connectionPageSize,
			PageToken: connectionPageToken,
		})
		if err != nil {
			return fmt.Errorf("failed to list connections: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

var getConnectionCmd = &cobra.Command{
	Use:   "get [tenant-id] [connection-id]",
	Short: "Get a connection",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, tenantAdmin, _, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := tenantAdmin.GetConnection(authenticatedContext(cmd.Context()), &v0.GetConnectionRequest{
			TenantId:     args[0],
			ConnectionId: args[1],
		})
		if err != nil {
			return fmt.Errorf("failed to get connection: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

var createConnectionCmd = &cobra.Command{
	Use:   "create [tenant-id]",
	Short: "Create a connection",
	Long: "Create a connection. The client secret is read from --client-secret or, when the flag is not set,\n" +
		"from the " + clientSecretEnv + " environment variable.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, tenantAdmin, _, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := tenantAdmin.CreateConnection(authenticatedContext(cmd.Context()), &v0.CreateConnectionRequest{
			TenantId:     args[0],
			Label:        connectionLabel,
			Issuer:       connectionIssuer,
			ClientId:     connectionClientID,
			ClientSecret: clientSecret(),
		})
		if err != nil {
			return fmt.Errorf("failed to create connection: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

var updateConnectionCmd = &cobra.Command{
	Use:   "update [tenant-id] [connection-id]",
	Short: "Change a connection's label or client secret",
	Long: "Change a connection's label or client secret: only what is given changes. The client secret is read\n" +
		"from --client-secret or, when the flag is not set, from the " + clientSecretEnv + " environment variable.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		update, mask := connectionUpdate(connectionLabel, clientSecret())
		if len(mask.GetPaths()) == 0 {
			return fmt.Errorf("nothing to update: give --label or a client secret")
		}

		closeConn, tenantAdmin, _, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := tenantAdmin.UpdateConnection(authenticatedContext(cmd.Context()), &v0.UpdateConnectionRequest{
			TenantId:     args[0],
			ConnectionId: args[1],
			Connection:   update,
			UpdateMask:   mask,
		})
		if err != nil {
			return fmt.Errorf("failed to update connection: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

var deleteConnectionCmd = &cobra.Command{
	Use:   "delete [connection-id]",
	Short: "Delete a connection, whichever tenant owns it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, _, platformAdmin, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		_, err = platformAdmin.DeleteAnyConnection(authenticatedContext(cmd.Context()), &v0.DeleteAnyConnectionRequest{
			ConnectionId: args[0],
		})
		if err != nil {
			return fmt.Errorf("failed to delete connection: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Connection deleted: %s\n", args[0])
		return nil
	},
}

var testConnectionCmd = &cobra.Command{
	Use:   "test [tenant-id] [connection-id]",
	Short: "Start a test sign-in and print the URL to open in a browser",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		closeConn, tenantAdmin, _, err := adminClients()
		if err != nil {
			return err
		}
		defer closeConn()

		resp, err := tenantAdmin.StartTestLogin(authenticatedContext(cmd.Context()), &v0.StartTestLoginRequest{
			TenantId:     args[0],
			ConnectionId: args[1],
		})
		if err != nil {
			return fmt.Errorf("failed to start the test sign-in: %w", err)
		}

		return printMessage(cmd, resp)
	},
}

func clientSecret() string {
	if connectionClientSecret != "" {
		return connectionClientSecret
	}

	return os.Getenv(clientSecretEnv)
}

// connectionUpdate names in the mask only what was given.
func connectionUpdate(label, secret string) (*v0.ConnectionUpdate, *fieldmaskpb.FieldMask) {
	update, mask := &v0.ConnectionUpdate{}, &fieldmaskpb.FieldMask{}
	if label != "" {
		update.Label = label
		mask.Paths = append(mask.Paths, "label")
	}
	if secret != "" {
		update.ClientSecret = secret
		mask.Paths = append(mask.Paths, "client_secret")
	}

	return update, mask
}

func init() {
	rootCmd.AddCommand(connectionCmd)
	connectionCmd.AddCommand(listConnectionsCmd)
	connectionCmd.AddCommand(getConnectionCmd)
	connectionCmd.AddCommand(createConnectionCmd)
	connectionCmd.AddCommand(updateConnectionCmd)
	connectionCmd.AddCommand(deleteConnectionCmd)
	connectionCmd.AddCommand(testConnectionCmd)

	listConnectionsCmd.Flags().Int32Var(&connectionPageSize, "page-size", 0, "Maximum number of results per page")
	listConnectionsCmd.Flags().StringVar(&connectionPageToken, "page-token", "", "Token of the page to return")

	for _, c := range []*cobra.Command{createConnectionCmd, updateConnectionCmd} {
		c.Flags().StringVar(&connectionLabel, "label", "", "Name the sign-in button shows")
		c.Flags().StringVar(&connectionClientSecret, "client-secret", "", "Client secret at the identity provider")
	}
	createConnectionCmd.Flags().StringVar(&connectionIssuer, "issuer", "", "The identity provider's OIDC issuer URL")
	createConnectionCmd.Flags().StringVar(&connectionClientID, "client-id", "", "Client id at the identity provider")
	for _, name := range []string{"label", "issuer", "client-id"} {
		_ = createConnectionCmd.MarkFlagRequired(name)
	}
}
