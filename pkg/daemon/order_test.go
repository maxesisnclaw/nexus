package daemon

import (
	"strings"
	"testing"

	"github.com/maxesisn/nexus/pkg/config"
)

func TestResolveStartOrderLinearChain(t *testing.T) {
	services := []config.ServiceSpec{
		{Name: "A", DependsOn: []string{"B"}},
		{Name: "B", DependsOn: []string{"C"}},
		{Name: "C"},
	}
	ordered, err := ResolveStartOrder(services)
	if err != nil {
		t.Fatalf("ResolveStartOrder() error = %v", err)
	}
	if got := names(ordered); got != "C,B,A" {
		t.Fatalf("unexpected order: %s", got)
	}
}

func TestResolveStartOrderDiamond(t *testing.T) {
	services := []config.ServiceSpec{
		{Name: "A", DependsOn: []string{"B", "C"}},
		{Name: "B", DependsOn: []string{"D"}},
		{Name: "C", DependsOn: []string{"D"}},
		{Name: "D"},
	}
	ordered, err := ResolveStartOrder(services)
	if err != nil {
		t.Fatalf("ResolveStartOrder() error = %v", err)
	}
	got := names(ordered)
	if !strings.HasPrefix(got, "D,") {
		t.Fatalf("expected D first, got %s", got)
	}
	if !strings.HasSuffix(got, ",A") {
		t.Fatalf("expected A last, got %s", got)
	}
}

func TestResolveStartOrderCycle(t *testing.T) {
	services := []config.ServiceSpec{
		{Name: "A", DependsOn: []string{"B"}},
		{Name: "B", DependsOn: []string{"A"}},
	}
	_, err := ResolveStartOrder(services)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "A -> B -> A") {
		t.Fatalf("expected cycle path in error, got %v", err)
	}
}

func TestResolveStartOrderMissingDependency(t *testing.T) {
	services := []config.ServiceSpec{
		{Name: "A", DependsOn: []string{"missing"}},
	}
	_, err := ResolveStartOrder(services)
	if err == nil {
		t.Fatal("expected missing dependency error")
	}
	want := "service A depends on unknown service missing"
	if err.Error() != want {
		t.Fatalf("unexpected error: got=%q want=%q", err.Error(), want)
	}
}

func TestResolveStartOrderNoDependenciesPreservesOrder(t *testing.T) {
	services := []config.ServiceSpec{
		{Name: "svc-1"},
		{Name: "svc-2"},
		{Name: "svc-3"},
	}
	ordered, err := ResolveStartOrder(services)
	if err != nil {
		t.Fatalf("ResolveStartOrder() error = %v", err)
	}
	if got := names(ordered); got != "svc-1,svc-2,svc-3" {
		t.Fatalf("unexpected order: %s", got)
	}
}

func names(items []config.ServiceSpec) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.Name)
	}
	return strings.Join(parts, ",")
}
