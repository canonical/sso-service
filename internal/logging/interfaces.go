// Copyright 2026 Canonical Ltd.
// SPDX-License-Identifier: AGPL-3.0-only

package logging

type LoggerInterface interface {
	Errorf(string, ...interface{})
	Infof(string, ...interface{})
	Warnf(string, ...interface{})
	Debugf(string, ...interface{})
	Fatalf(string, ...interface{})
	Error(...interface{})
	Info(...interface{})
	Warn(...interface{})
	Debug(...interface{})
	Fatal(...interface{})
	// Errorw, Infow, Warnw, Debugw emit structured key-value log entries using
	// zap SugaredLogger's "w"-suffix convention: Errorw(msg, key, val, ...).
	Errorw(string, ...interface{})
	Infow(string, ...interface{})
	Warnw(string, ...interface{})
	Debugw(string, ...interface{})
	Security() SecurityLoggerInterface
}

type SecurityLoggerInterface interface {
	SuccessfulLogin(string, ...Option)
	FailedLogin(string, ...Option)
	AccountLockout(string, ...Option)
	PasswordChange(string, ...Option)
	PasswordChangeFail(string, ...Option)
	TokenCreate(...Option)
	TokenRevoke(...Option)
	TokenReuse(string, ...Option)
	TokenDelete(string, ...Option)
	AdminAction(string, string, string, string, ...Option)
	AuthzFailure(string, string, ...Option)
	AuthzFailureNotEmployee(string, ...Option)
	AuthzFailureApplicationAccess(string, string, ...Option)
	AuthzFailureNoSession(string, ...Option)
	AuthzFailureInsufficientPermissions(string, string, string, ...Option)
	AuthzFailureRoleAssignment(string, string, ...Option)
	AuthzFailureIdentityAssignment(string, string, ...Option)
	SystemStartup(...Option)
	SystemShutdown(...Option)
	SystemRestart(...Option)
	SystemCrash(...Option)
}
