package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
)

func TestParseHealthProbeExecHTTPAndTCP(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want any
	}{
		{name: "exec", raw: "exec:///bin/sh -c true", want: &execProbe{}},
		{name: "http", raw: "http://127.0.0.1:8080/health", want: &httpProbe{}},
		{name: "tcp", raw: "tcp://127.0.0.1:8080", want: &tcpProbe{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe, err := parseHealthProbe(tc.raw)
			if err != nil {
				t.Fatalf("parseHealthProbe() error = %v", err)
			}
			switch tc.want.(type) {
			case *execProbe:
				if _, ok := probe.(*execProbe); !ok {
					t.Fatalf("expected execProbe, got %T", probe)
				}
			case *httpProbe:
				if _, ok := probe.(*httpProbe); !ok {
					t.Fatalf("expected httpProbe, got %T", probe)
				}
			case *tcpProbe:
				if _, ok := probe.(*tcpProbe); !ok {
					t.Fatalf("expected tcpProbe, got %T", probe)
				}
			}
		})
	}
}

func TestParseHealthProbeRejectsInvalid(t *testing.T) {
	if _, err := parseHealthProbe("bad://test"); err == nil {
		t.Fatal("expected invalid scheme error")
	}
	if _, err := parseHealthProbe("exec://"); err == nil {
		t.Fatal("expected exec command missing error")
	}
}

func TestExecProbeCheck(t *testing.T) {
	success := &execProbe{command: "/bin/sh", args: []string{"-c", "exit 0"}}
	if err := success.Check(context.Background()); err != nil {
		t.Fatalf("execProbe success check error = %v", err)
	}

	failure := &execProbe{command: "/bin/sh", args: []string{"-c", "exit 1"}}
	if err := failure.Check(context.Background()); err == nil {
		t.Fatal("expected execProbe failure")
	}
}

func TestHTTPProbeCheck(t *testing.T) {
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okServer.Close()

	okProbe := &httpProbe{url: okServer.URL}
	if err := okProbe.Check(context.Background()); err != nil {
		t.Fatalf("httpProbe success check error = %v", err)
	}

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer badServer.Close()

	badProbe := &httpProbe{url: badServer.URL}
	if err := badProbe.Check(context.Background()); err == nil {
		t.Fatal("expected httpProbe failure")
	}
}

func TestTCPProbeCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	probe := &tcpProbe{addr: ln.Addr().String()}
	if err := probe.Check(context.Background()); err != nil {
		t.Fatalf("tcpProbe success check error = %v", err)
	}

	_ = ln.Close()
	if err := probe.Check(context.Background()); err == nil {
		t.Fatal("expected tcpProbe failure after close")
	}
}

func TestHealthMonitorUsesProbeWhenConfigured(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	fake := &fakeDockerRuntime{running: map[string]bool{"nexus-worker": true}}
	pm.docker = fake
	pm.procs["worker"] = &ManagedProcess{
		ID:            "worker",
		Service:       "worker",
		Spec:          config.ServiceSpec{Name: "worker", Runtime: "docker", Image: "repo/worker:latest", HealthCheck: "tcp://127.0.0.1:1"},
		containerName: "nexus-worker",
	}

	h, err := NewHealthMonitor(logger, pm, 100*time.Millisecond, 5, []config.ServiceSpec{{
		Name:        "worker",
		Runtime:     "docker",
		Type:        "singleton",
		Image:       "repo/worker:latest",
		HealthCheck: "tcp://127.0.0.1:1",
	}})
	if err != nil {
		t.Fatalf("NewHealthMonitor() error = %v", err)
	}
	h.baseBackoff = 0
	h.checkOnce(context.Background())

	if fake.startCalls != 1 {
		t.Fatalf("expected restart triggered by failed probe, got startCalls=%d", fake.startCalls)
	}
}

func TestHealthMonitorFallbackWithoutProbe(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	fake := &fakeDockerRuntime{running: map[string]bool{"nexus-worker": true}}
	pm.docker = fake
	pm.procs["worker"] = &ManagedProcess{
		ID:            "worker",
		Service:       "worker",
		Spec:          config.ServiceSpec{Name: "worker", Runtime: "docker", Image: "repo/worker:latest"},
		containerName: "nexus-worker",
	}

	h, err := NewHealthMonitor(logger, pm, 100*time.Millisecond, 5, []config.ServiceSpec{{
		Name:    "worker",
		Runtime: "docker",
		Type:    "singleton",
		Image:   "repo/worker:latest",
	}})
	if err != nil {
		t.Fatalf("NewHealthMonitor() error = %v", err)
	}
	h.baseBackoff = 0
	h.checkOnce(context.Background())

	if fake.startCalls != 0 {
		t.Fatalf("expected no restart when probe is not configured, got startCalls=%d", fake.startCalls)
	}
}
