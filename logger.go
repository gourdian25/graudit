// File: logger.go

package graudit

// Logger is the minimal logging interface graudit backends accept for
// optional diagnostic logging (constructor connectivity failures, grevents
// publish failures, shutdown). It is satisfied structurally by
// *grlog.Logger's printf-style methods (Infof/Warnf/Errorf) — graudit
// itself does not import grlog, so plugging in a logger is entirely opt-in
// and adds no dependency for consumers who don't want one.
//
// A nil Logger passed to any backend's Config/Option is replaced with
// NopLogger() — logging is always optional, never required for a backend
// to function.
//
// Example:
//
//	import "github.com/gourdian25/grlog"
//
//	logger := grlog.NewDefaultLogger()
//	log, err := postgres.NewPostgresAuditLog(postgres.PostgresConfig{
//		DSN:    dsn,
//		Logger: logger, // *grlog.Logger satisfies graudit.Logger directly
//	})
type Logger interface {
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type noopLogger struct{}

func (noopLogger) Infof(string, ...interface{})  {}
func (noopLogger) Warnf(string, ...interface{})  {}
func (noopLogger) Errorf(string, ...interface{}) {}

// NopLogger returns a Logger that discards every message. It is the default
// used by every backend when no Logger is configured.
func NopLogger() Logger { return noopLogger{} }

// OrNop returns l if it is non-nil, otherwise NopLogger(). Backends call
// this once at construction time so every subsequent log call site can
// assume a non-nil Logger.
func OrNop(l Logger) Logger {
	if l == nil {
		return NopLogger()
	}
	return l
}
