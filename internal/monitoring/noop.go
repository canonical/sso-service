// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package monitoring

import (
	"github.com/canonical/sso-service/internal/logging"
)

type NoopMonitor struct {
	service string

	logger logging.LoggerInterface
}

func NewNoopMonitor(service string, logger logging.LoggerInterface) *NoopMonitor {
	m := new(NoopMonitor)
	m.service = service
	m.logger = logger
	return m
}

func (m *NoopMonitor) GetService() string {
	return m.service
}
func (m *NoopMonitor) SetResponseTimeMetric(map[string]string, float64) error {
	return nil
}
func (m *NoopMonitor) SetStorageResponseTimeMetric(map[string]string, float64) error {
	return nil
}
func (m *NoopMonitor) IncrementCallbackOutcomes(map[string]string) error {
	return nil
}
func (m *NoopMonitor) IncrementFirstSignIns(map[string]string) error {
	return nil
}
func (m *NoopMonitor) IncrementAttemptsCompleted() error {
	return nil
}
func (m *NoopMonitor) IncrementCompleteAttemptRefused() error {
	return nil
}
func (m *NoopMonitor) IncrementTenantServiceWriteFailures(map[string]string) error {
	return nil
}
