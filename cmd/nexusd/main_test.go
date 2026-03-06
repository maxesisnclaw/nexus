package main

import (
	"context"
	"log/slog"
	"testing"
)

func TestNewLoggerLevelMapping(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		debugOn bool
		errorOn bool
	}{
		{name: "debug", level: "debug", debugOn: true, errorOn: true},
		{name: "warn", level: "warn", debugOn: false, errorOn: true},
		{name: "error", level: "error", debugOn: false, errorOn: true},
		{name: "default", level: "unknown", debugOn: false, errorOn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger := newLogger(tc.level)
			ctx := context.Background()
			if got := logger.Enabled(ctx, slog.LevelDebug); got != tc.debugOn {
				t.Fatalf("debug enabled mismatch: got=%v want=%v", got, tc.debugOn)
			}
			if got := logger.Enabled(ctx, slog.LevelError); got != tc.errorOn {
				t.Fatalf("error enabled mismatch: got=%v want=%v", got, tc.errorOn)
			}
		})
	}
}
