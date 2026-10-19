// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package tracing

import (
	"context"
	"testing"

	"github.com/canonical/sso-service/internal/logging"
)

func TestNewTracer_ExporterNotCreated(t *testing.T) {
	// No exporter can be created for an endpoint that is not an address.
	tracer := NewTracer(NewConfig(true, "\x00", "", logging.NewNoopLogger()))
	if tracer == nil {
		t.Fatal("expected a tracer")
	}

	_, span := tracer.Start(context.Background(), "test")
	defer span.End()
	if span.IsRecording() {
		t.Error("expected the no-op tracer")
	}
}
