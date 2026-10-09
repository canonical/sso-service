// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package monitoring

type MonitorInterface interface {
	GetService() string
	SetResponseTimeMetric(map[string]string, float64) error
	SetStorageResponseTimeMetric(map[string]string, float64) error
	// Expected tags: "reason".
	IncrementCallbackOutcomes(map[string]string) error
	// Expected tags: "outcome".
	IncrementFirstSignIns(map[string]string) error
	IncrementAttemptsCompleted() error
	IncrementCompleteAttemptRefused() error
	// Expected tags: "rpc".
	IncrementTenantServiceWriteFailures(map[string]string) error
}
