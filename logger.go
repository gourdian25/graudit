// File: logger.go

package graudit

// Logger is the minimal logging interface graudit backends accept for
// optional diagnostic logging (constructor connectivity failures, grevents
// publish failures, shutdown). Its four methods match *slog.Logger's own
// signatures exactly, so *slog.Logger satisfies it structurally — graudit
// itself does not import grlog or log/slog, so plugging in a logger is
// entirely opt-in and adds no dependency for consumers who don't want one.
//
// A nil Logger passed to any backend's Config/Option is replaced with
// NopLogger() — logging is always optional, never required for a backend
// to function.
//
// Example, using grlog via its log/slog adapter (the recommended bridge —
// grlog itself needs no code changes for this):
//
//	import (
//		"log/slog"
//
//		"github.com/gourdian25/grlog"
//	)
//
//	logger := slog.New(grlog.NewSlogHandler(grlog.NewDefaultLogger()))
//	log, err := graudit.NewPostgresAuditLog(graudit.PostgresConfig{
//		DSN:    dsn,
//		Logger: logger, // *slog.Logger satisfies graudit.Logger directly
//	})
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Info(string, ...any)  {}
func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Error(string, ...any) {}

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
