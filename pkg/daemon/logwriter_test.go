package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestPrefixWriterFlushesCompleteAndPartialLines(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	w := &prefixWriter{logger: logger, id: "svc-1", stream: "stdout"}

	if _, err := w.Write([]byte("first\nsecond")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	w.Flush()

	records := decodeJSONLogLines(t, buf.String())
	if len(records) != 2 {
		t.Fatalf("expected 2 log records, got %d", len(records))
	}
	if got := records[0]["line"]; got != "first" {
		t.Fatalf("unexpected first line: %v", got)
	}
	if got := records[1]["line"]; got != "second" {
		t.Fatalf("unexpected second line: %v", got)
	}
	if got := records[0]["stream"]; got != "stdout" {
		t.Fatalf("unexpected stream: %v", got)
	}
	if got := records[0]["level"]; got != "INFO" {
		t.Fatalf("unexpected level: %v", got)
	}
}

func TestPrefixWriterUsesWarnForStderr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	w := &prefixWriter{logger: logger, id: "svc-2", stream: "stderr"}

	if _, err := w.Write([]byte("boom\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	records := decodeJSONLogLines(t, buf.String())
	if len(records) != 1 {
		t.Fatalf("expected 1 log record, got %d", len(records))
	}
	if got := records[0]["line"]; got != "boom" {
		t.Fatalf("unexpected line: %v", got)
	}
	if got := records[0]["level"]; got != "WARN" {
		t.Fatalf("unexpected level: %v", got)
	}
}

func decodeJSONLogLines(t *testing.T, input string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(input), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		entry := make(map[string]any)
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("json unmarshal log line: %v", err)
		}
		out = append(out, entry)
	}
	return out
}
