// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"fmt"
	"strings"

	v0 "github.com/canonical/identity-platform-api/v0/sso"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	authToken    string
	grpcEndpoint string
)

// adminClients dials the running service: the management commands are its API's clients.
func adminClients() (func() error, v0.SSOTenantAdminServiceClient, v0.SSOPlatformAdminServiceClient, error) {
	conn, err := grpc.NewClient(grpcEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to dial gRPC server: %w", err)
	}

	return conn.Close, v0.NewSSOTenantAdminServiceClient(conn), v0.NewSSOPlatformAdminServiceClient(conn), nil
}

func authenticatedContext(ctx context.Context) context.Context {
	if authToken == "" {
		return ctx
	}

	token := strings.TrimPrefix(authToken, "Bearer ")
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

// printMessage writes a response as JSON, the form the API's HTTP gateway answers in.
func printMessage(cmd *cobra.Command, m proto.Message) error {
	out, err := protojson.MarshalOptions{Multiline: true, EmitUnpopulated: true}.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed to encode the response: %w", err)
	}

	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
	return err
}

func init() {
	rootCmd.PersistentFlags().StringVar(&grpcEndpoint, "grpc-endpoint", "localhost:50051", "gRPC server endpoint")
	rootCmd.PersistentFlags().StringVar(&authToken, "token", "", "Authorization token (e.g. Bearer <token>)")
}
