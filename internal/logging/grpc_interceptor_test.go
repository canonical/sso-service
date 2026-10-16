// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:generate mockgen -build_flags=--mod=mod -package logging -destination ./mock_logger_test.go -source=./interfaces.go

func TestLoggingUnaryInterceptor(t *testing.T) {
	tests := []struct {
		name       string
		fullMethod string
		handlerErr error
		wantCode   codes.Code
		wantLevel  string // "debug" or "error"
	}{
		{
			name:       "successful call logs at Debug",
			fullMethod: "/test.Service/GetItem",
			handlerErr: nil,
			wantCode:   codes.OK,
			wantLevel:  "debug",
		},
		{
			name:       "client error logs at Debug",
			fullMethod: "/test.Service/CreateItem",
			handlerErr: status.Error(codes.NotFound, "not found"),
			wantCode:   codes.NotFound,
			wantLevel:  "debug",
		},
		{
			name:       "server Internal error logs at Error",
			fullMethod: "/test.Service/CreateItem",
			handlerErr: status.Error(codes.Internal, "db failure"),
			wantCode:   codes.Internal,
			wantLevel:  "error",
		},
		{
			name:       "Unavailable logs at Error",
			fullMethod: "/test.Service/CreateItem",
			handlerErr: status.Error(codes.Unavailable, "service down"),
			wantCode:   codes.Unavailable,
			wantLevel:  "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			mockLogger := NewMockLoggerInterface(ctrl)

			switch tt.wantLevel {
			case "error":
				mockLogger.EXPECT().Errorf(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(1)
			case "debug":
				mockLogger.EXPECT().Debugf(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(1)
			}

			interceptor := LoggingUnaryInterceptor(mockLogger)

			handler := func(ctx context.Context, req interface{}) (interface{}, error) {
				return "response", tt.handlerErr
			}

			info := &grpc.UnaryServerInfo{FullMethod: tt.fullMethod}
			resp, err := interceptor(context.Background(), "request", info, handler)

			if resp != "response" {
				t.Errorf("resp = %v, want %q", resp, "response")
			}
			if tt.handlerErr == nil && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if tt.handlerErr != nil && status.Code(err) != tt.wantCode {
				t.Errorf("err code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}
