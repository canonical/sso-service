// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package prometheus

import (
	"fmt"

	"github.com/canonical/sso-service/internal/logging"
	"github.com/prometheus/client_golang/prometheus"
)

type Monitor struct {
	service string

	responseTime        *prometheus.HistogramVec
	storageResponseTime *prometheus.HistogramVec

	callbackOutcomes           *prometheus.CounterVec
	firstSignIns               *prometheus.CounterVec
	attemptsCompleted          *prometheus.CounterVec
	completeAttemptRefused     *prometheus.CounterVec
	tenantServiceWriteFailures *prometheus.CounterVec

	logger logging.LoggerInterface
}

func (m *Monitor) GetService() string {
	return m.service
}

func (m *Monitor) SetResponseTimeMetric(tags map[string]string, value float64) error {
	if m.responseTime == nil {
		return fmt.Errorf("metric not instantiated")
	}

	m.responseTime.With(tags).Observe(value)

	return nil
}

func (m *Monitor) SetStorageResponseTimeMetric(tags map[string]string, value float64) error {
	if m.storageResponseTime == nil {
		return fmt.Errorf("metric not instantiated")
	}

	m.storageResponseTime.With(tags).Observe(value)

	return nil
}

func (m *Monitor) IncrementCallbackOutcomes(tags map[string]string) error {
	if m.callbackOutcomes == nil {
		return fmt.Errorf("metric not instantiated")
	}

	m.callbackOutcomes.With(tags).Inc()

	return nil
}

func (m *Monitor) IncrementFirstSignIns(tags map[string]string) error {
	if m.firstSignIns == nil {
		return fmt.Errorf("metric not instantiated")
	}

	m.firstSignIns.With(tags).Inc()

	return nil
}

func (m *Monitor) IncrementAttemptsCompleted() error {
	if m.attemptsCompleted == nil {
		return fmt.Errorf("metric not instantiated")
	}

	m.attemptsCompleted.With(nil).Inc()

	return nil
}

func (m *Monitor) IncrementCompleteAttemptRefused() error {
	if m.completeAttemptRefused == nil {
		return fmt.Errorf("metric not instantiated")
	}

	m.completeAttemptRefused.With(nil).Inc()

	return nil
}

func (m *Monitor) IncrementTenantServiceWriteFailures(tags map[string]string) error {
	if m.tenantServiceWriteFailures == nil {
		return fmt.Errorf("metric not instantiated")
	}

	m.tenantServiceWriteFailures.With(tags).Inc()

	return nil
}

func (m *Monitor) registerHistograms() {
	histograms := make([]*prometheus.HistogramVec, 0)

	labels := map[string]string{
		"service": m.service,
	}

	m.responseTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "http_response_time_seconds",
			Help:        "http_response_time_seconds",
			ConstLabels: labels,
		},
		[]string{"route", "status"},
	)

	m.storageResponseTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "storage_query_duration_seconds",
			Help:        "storage_query_duration_seconds records the duration of database queries",
			ConstLabels: labels,
		},
		[]string{"operation", "status"},
	)

	histograms = append(histograms, m.responseTime, m.storageResponseTime)

	for _, histogram := range histograms {
		err := prometheus.Register(histogram)

		switch err.(type) {
		case nil:
			continue
		case prometheus.AlreadyRegisteredError:
			m.logger.Debugf("metric %v already registered", histogram)
		default:
			m.logger.Errorf("metric %v could not be registered", histogram)
		}
	}
}

func (m *Monitor) registerCounters() {
	counters := make([]*prometheus.CounterVec, 0)

	labels := map[string]string{
		"service": m.service,
	}

	m.callbackOutcomes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "sso_callback_outcomes_total",
			Help:        "Company sign-in callback outcomes by reason.",
			ConstLabels: labels,
		},
		[]string{"reason"},
	)

	m.firstSignIns = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "sso_first_sign_ins_total",
			Help:        "First company sign-ins (a subject with no link) let through to Kratos at /callback, by outcome: account_linking, registration.",
			ConstLabels: labels,
		},
		[]string{"outcome"},
	)

	m.attemptsCompleted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "sso_attempts_completed_total",
			Help:        "CompleteAttempt calls that confirmed a sign-in attempt.",
			ConstLabels: labels,
		},
		[]string{},
	)

	m.completeAttemptRefused = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "sso_complete_attempt_refused_total",
			Help:        "CompleteAttempt calls refused as NOT_APPLICABLE.",
			ConstLabels: labels,
		},
		[]string{},
	)

	m.tenantServiceWriteFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "sso_tenant_service_write_failures_total",
			Help:        "Failed writes to tenant-service by RPC.",
			ConstLabels: labels,
		},
		[]string{"rpc"},
	)

	counters = append(
		counters,
		m.callbackOutcomes,
		m.firstSignIns,
		m.attemptsCompleted,
		m.completeAttemptRefused,
		m.tenantServiceWriteFailures,
	)

	for _, counter := range counters {
		err := prometheus.Register(counter)

		switch err.(type) {
		case nil:
			continue
		case prometheus.AlreadyRegisteredError:
			m.logger.Debugf("metric %v already registered", counter)
		default:
			m.logger.Errorf("metric %v could not be registered", counter)
		}
	}
}

func NewMonitor(service string, logger logging.LoggerInterface) *Monitor {
	m := new(Monitor)

	m.service = service
	m.logger = logger

	m.registerHistograms()
	m.registerCounters()

	return m
}
