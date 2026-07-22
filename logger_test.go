// File: logger_test.go

package graudit_test

// This file proves *slog.Logger satisfies graudit.Logger structurally,
// without graudit itself importing grlog or log/slog — grlog is a
// test-only dependency of this module, so it never leaks into consumers
// who only import graudit and don't want a logging dependency at all.
// Mirrors grcache's own logger_test.go exactly.

import (
	"log/slog"
	"testing"

	"github.com/gourdian25/grlog"

	"github.com/gourdian25/graudit"
)

var _ graudit.Logger = (*slog.Logger)(nil)

func TestGrlogSatisfiesLoggerInterface(t *testing.T) {
	logger := grlog.NewDefaultLogger()
	defer func() { _ = logger.Close() }()

	slogger := slog.New(grlog.NewSlogHandler(logger))
	var l graudit.Logger = slogger

	l.Debug("graudit test", "level", "debug")
	l.Info("graudit test", "level", "info")
	l.Warn("graudit test", "level", "warn")
	l.Error("graudit test", "level", "error")
}

func TestOrNop_WithGrlog(t *testing.T) {
	logger := grlog.NewDefaultLogger()
	defer func() { _ = logger.Close() }()
	slogger := slog.New(grlog.NewSlogHandler(logger))
	if graudit.OrNop(slogger) != graudit.Logger(slogger) {
		t.Fatal("OrNop(non-nil) did not return the given logger unchanged")
	}
}
