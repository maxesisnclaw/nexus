package registry

import (
	"testing"
	"time"
)

func TestRegisterLookupAndCapability(t *testing.T) {
	r := NewWithOptions("node-a", 200*time.Millisecond, 20*time.Millisecond)
	defer r.Close()

	r.Register(ServiceInstance{
		Name:         "detector",
		ID:           "detector-vehicle",
		Capabilities: []string{"detect-vehicle"},
		Endpoints:    []Endpoint{{Type: EndpointUDS, Addr: "/run/nexus/svc/detector.sock"}},
	})
	r.Register(ServiceInstance{
		Name:         "detector",
		ID:           "detector-person",
		Capabilities: []string{"detect-person"},
		Endpoints:    []Endpoint{{Type: EndpointTCP, Addr: "127.0.0.1:9100"}},
	})

	byName := r.Lookup("detector")
	if len(byName) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(byName))
	}
	byCap := r.LookupByCapability("detect-person")
	if len(byCap) != 1 || byCap[0].ID != "detector-person" {
		t.Fatalf("unexpected capability lookup: %+v", byCap)
	}
}

func TestHeartbeatPreventsExpiry(t *testing.T) {
	r := NewWithOptions("node-a", 100*time.Millisecond, 10*time.Millisecond)
	defer r.Close()

	r.Register(ServiceInstance{Name: "svc", ID: "svc-1"})
	time.Sleep(60 * time.Millisecond)
	if ok := r.Heartbeat("svc-1"); !ok {
		t.Fatal("expected heartbeat success")
	}
	time.Sleep(60 * time.Millisecond)
	if got := len(r.Lookup("svc")); got != 1 {
		t.Fatalf("expected instance alive after heartbeat, got %d", got)
	}
}

func TestExpiryAndWatch(t *testing.T) {
	r := NewWithOptions("node-a", 50*time.Millisecond, 10*time.Millisecond)
	defer r.Close()

	events := make(chan ChangeEvent, 4)
	unsubscribe := r.Watch("svc", func(ev ChangeEvent) {
		events <- ev
	})
	defer unsubscribe()

	r.Register(ServiceInstance{Name: "svc", ID: "svc-1", TTL: 40 * time.Millisecond})

	up := <-events
	if up.Type != ChangeUp {
		t.Fatalf("expected up event, got %s", up.Type)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-events:
			if ev.Type == ChangeDown && ev.Instance.ID == "svc-1" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for down event")
		}
	}
}

func TestMergeSnapshotUsesLatestUpdate(t *testing.T) {
	r := NewWithOptions("node-a", 1*time.Second, 200*time.Millisecond)
	defer r.Close()

	old := ServiceInstance{Name: "svc", ID: "svc-1", UpdatedAt: time.Now().Add(-10 * time.Second)}
	newer := ServiceInstance{Name: "svc", ID: "svc-1", UpdatedAt: time.Now().Add(10 * time.Second), Node: "node-b"}

	r.MergeSnapshot([]ServiceInstance{old})
	if got := r.Lookup("svc"); len(got) != 1 || got[0].Node != "" {
		t.Fatalf("unexpected after old merge: %+v", got)
	}

	r.MergeSnapshot([]ServiceInstance{newer})
	got := r.Lookup("svc")
	if len(got) != 1 || got[0].Node != "node-b" {
		t.Fatalf("expected newer merge to win, got %+v", got)
	}
}

func TestDiscoveryPickRoundRobin(t *testing.T) {
	r := NewWithOptions("node-a", 1*time.Second, 200*time.Millisecond)
	defer r.Close()

	r.Register(ServiceInstance{Name: "svc", ID: "a"})
	r.Register(ServiceInstance{Name: "svc", ID: "b"})

	d := NewDiscovery(r)
	first, err := d.Pick("svc")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	second, err := d.Pick("svc")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("expected round robin picks to differ, first=%s second=%s", first.ID, second.ID)
	}
}

func TestUnregisterAndUnsubscribe(t *testing.T) {
	r := NewWithOptions("node-a", 200*time.Millisecond, 20*time.Millisecond)
	defer r.Close()

	events := make(chan ChangeEvent, 4)
	unsub := r.Watch("svc", func(ev ChangeEvent) { events <- ev })
	r.Register(ServiceInstance{Name: "svc", ID: "svc-1"})
	r.Unregister("svc-1")
	unsub()
	r.Register(ServiceInstance{Name: "svc", ID: "svc-2"})

	close(events)
	var got []ChangeType
	for ev := range events {
		got = append(got, ev.Type)
	}
	if len(got) != 2 || got[0] != ChangeUp || got[1] != ChangeDown {
		t.Fatalf("unexpected watch event sequence: %+v", got)
	}
}

func TestSnapshotSorted(t *testing.T) {
	r := NewWithOptions("node-a", time.Second, 100*time.Millisecond)
	defer r.Close()

	r.Register(ServiceInstance{Name: "svc", ID: "b"})
	r.Register(ServiceInstance{Name: "svc", ID: "a"})
	snapshot := r.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("unexpected snapshot length: %d", len(snapshot))
	}
	if snapshot[0].ID != "a" || snapshot[1].ID != "b" {
		t.Fatalf("snapshot should be sorted by ID, got %+v", snapshot)
	}
}

func TestDiscoveryErrors(t *testing.T) {
	r := NewWithOptions("node-a", time.Second, 100*time.Millisecond)
	defer r.Close()
	d := NewDiscovery(r)
	if _, err := d.Pick("missing"); err == nil {
		t.Fatal("expected Pick error for missing service")
	}
	if _, err := d.PickByCapability("missing-cap"); err == nil {
		t.Fatal("expected PickByCapability error for missing capability")
	}
}
