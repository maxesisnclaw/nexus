package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestMainRuns(t *testing.T) {
	output := captureStdout(t, main)
	if !strings.Contains(output, "pipeline result: [pipeline] nexus pipeline") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = orig
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("stdout writer close error = %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	return string(data)
}
