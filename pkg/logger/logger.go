package logger

import (
	stdlog "log"
	"strings"
)

// Sync keeps compatibility with previous logger usage.
// Standard library log has no buffered sync step.
func Sync() {}

func shouldSkipDebugLog(format string) bool {
	// Runtime debug tags are suppressed to reduce noisy logs in production runs.
	return strings.Contains(format, "[SaveWorkflow]") ||
		strings.Contains(format, "[PutWorkflow]") ||
		strings.Contains(format, "[DeleteWorkflow]")
}

func Infof(format string, args ...any) {
	if shouldSkipDebugLog(format) {
		return
	}
	stdlog.Printf("[INFO] "+format, args...)
}

func Warnf(format string, args ...any) {
	if shouldSkipDebugLog(format) {
		return
	}
	stdlog.Printf("[WARN] "+format, args...)
}

func Errorf(format string, args ...any) {
	if shouldSkipDebugLog(format) {
		return
	}
	stdlog.Printf("[ERROR] "+format, args...)
}

func Fatal(args ...any) {
	stdlog.Fatal(args...)
}

func Fatalf(format string, args ...any) {
	stdlog.Fatalf(format, args...)
}

// *Contextf helpers accept any ctx type to avoid coupling to a specific context implementation.
func InfoContextf(_ any, format string, args ...any) {
	Infof(format, args...)
}

func ErrorContextf(_ any, format string, args ...any) {
	Errorf(format, args...)
}
