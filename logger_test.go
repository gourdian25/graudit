// File: logger_test.go

package graudit_test

// This file proves *grlog.Logger satisfies graudit.Logger structurally,
// without graudit itself importing grlog — grlog is a test-only dependency
// of this module, so it never leaks into consumers who only import
// graudit/memory (or any other backend) and don't want a logging
// dependency at all. Mirrors grcache's own logger_test.go exactly.

import (
	"testing"

	"github.com/gourdian25/grlog"

	"github.com/gourdian25/graudit"
)

var _ graudit.Logger = (*grlog.Logger)(nil)

func TestGrlogSatisfiesLoggerInterface(t *testing.T) {
	logger := grlog.NewDefaultLogger()

	var l graudit.Logger = logger
	l.Infof("graudit test: %s", "info")
	l.Warnf("graudit test: %s", "warn")
	l.Errorf("graudit test: %s", "error")
}

func TestOrNop_WithGrlog(t *testing.T) {
	logger := grlog.NewDefaultLogger()
	if graudit.OrNop(logger) != graudit.Logger(logger) {
		t.Fatal("OrNop(non-nil) did not return the given logger unchanged")
	}
}
