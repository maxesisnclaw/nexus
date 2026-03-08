package registry

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestRegisterLookupAndCapability(t *testing.T) {
	r := NewWithOptions("node-a", 200*time.Millisecond, 20*time.Millisecond)
	defer r.Close()

	_ = r.Register(ServiceInstance{
		Name:         "detector",
		ID:           "detector-vehicle",
		Capabilities: []string{"detect-vehicle"},
		Endpoints:    []Endpoint{{Type: EndpointUDS, Addr: "/run/nexus/svc/detector.sock"}},
	})
	_ = r.Register(ServiceInstance{
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

	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-1"})
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

	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-1", TTL: 40 * time.Millisecond})

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

	_ = r.Register(ServiceInstance{Name: "svc", ID: "a"})
	_ = r.Register(ServiceInstance{Name: "svc", ID: "b"})

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
	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-1"})
	r.Unregister("svc-1")
	unsub()
	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-2"})

	got := make([]ChangeEvent, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case ev := <-events:
			got = append(got, ev)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for watch event %d", i)
		}
	}
	var up, down bool
	for _, ev := range got {
		if ev.Instance.ID != "svc-1" {
			t.Fatalf("unexpected event instance: %+v", ev)
		}
		if ev.Type == ChangeUp {
			up = true
		}
		if ev.Type == ChangeDown {
			down = true
		}
	}
	if !up || !down {
		t.Fatalf("unexpected watch event sequence: %+v", got)
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected event after unsubscribe: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWatchPanicDoesNotBreakDelivery(t *testing.T) {
	r := NewWithOptions("node-a", 200*time.Millisecond, 20*time.Millisecond)
	defer r.Close()

	events := make(chan ChangeEvent, 1)
	r.Watch("svc", func(ChangeEvent) {
		panic("watcher panic")
	})
	r.Watch("svc", func(ev ChangeEvent) {
		events <- ev
	})

	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-1"})

	select {
	case ev := <-events:
		if ev.Type != ChangeUp || ev.Instance.ID != "svc-1" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for non-panicking watcher event")
	}
}

func TestWatchBlockedCallbackDoesNotBlockOthers(t *testing.T) {
	r := NewWithOptions("node-a", 200*time.Millisecond, 20*time.Millisecond)
	defer r.Close()

	block := make(chan struct{})
	defer close(block)

	events := make(chan ChangeEvent, 1)
	r.Watch("svc", func(ChangeEvent) {
		<-block
	})
	r.Watch("svc", func(ev ChangeEvent) {
		events <- ev
	})

	done := make(chan struct{})
	go func() {
		_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-1"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("register blocked by watcher callback")
	}

	select {
	case ev := <-events:
		if ev.Type != ChangeUp || ev.Instance.ID != "svc-1" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unblocked watcher")
	}
}

func TestWatchSlowCallbackBoundedGoroutines(t *testing.T) {
	r := NewWithOptions("node-a", 200*time.Millisecond, 20*time.Millisecond)
	defer r.Close()

	block := make(chan struct{})
	defer close(block)

	started := make(chan struct{})
	var once sync.Once
	r.Watch("svc", func(ChangeEvent) {
		once.Do(func() { close(started) })
		<-block
	})

	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-0"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked watcher to start")
	}

	baseline := runtime.NumGoroutine()
	for i := 1; i <= 256; i++ {
		_ = r.Register(ServiceInstance{Name: "svc", ID: fmt.Sprintf("svc-%d", i)})
	}

	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	if delta := after - baseline; delta > 16 {
		t.Fatalf("too many goroutines for slow watcher: baseline=%d after=%d delta=%d", baseline, after, delta)
	}
}

func TestWatchUnsubscribeStopsQueuedDelivery(t *testing.T) {
	r := NewWithOptions("node-a", 200*time.Millisecond, 20*time.Millisecond)
	defer r.Close()

	block := make(chan struct{})
	started := make(chan struct{})
	events := make(chan ChangeEvent, 8)
	var once sync.Once

	unsub := r.Watch("svc", func(ev ChangeEvent) {
		once.Do(func() { close(started) })
		<-block
		events <- ev
	})

	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-1"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watcher callback start")
	}

	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-2"})
	unsub()
	_ = r.Register(ServiceInstance{Name: "svc", ID: "svc-3"})
	close(block)

	received := make([]ChangeEvent, 0, 2)
	deadline := time.After(time.Second)
collect:
	for {
		select {
		case ev := <-events:
			received = append(received, ev)
			if len(received) == 2 {
				break collect
			}
		case <-deadline:
			break collect
		}
	}

	if len(received) == 0 {
		t.Fatal("expected in-flight watcher callback delivery")
	}
	if len(received) > 2 {
		t.Fatalf("unexpected number of delivered events after unsubscribe: %d", len(received))
	}

	for _, ev := range received {
		if ev.Instance.ID == "svc-3" {
			t.Fatalf("unexpected event after unsubscribe: %+v", ev)
		}
		if ev.Instance.ID != "svc-1" && ev.Instance.ID != "svc-2" {
			t.Fatalf("unexpected delivered event: %+v", ev)
		}
	}

	select {
	case ev := <-events:
		t.Fatalf("unexpected extra event after unsubscribe: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSnapshotSorted(t *testing.T) {
	r := NewWithOptions("node-a", time.Second, 100*time.Millisecond)
	defer r.Close()

	_ = r.Register(ServiceInstance{Name: "svc", ID: "b"})
	_ = r.Register(ServiceInstance{Name: "svc", ID: "a"})
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
	d.mu.Lock()
	d.offset["missing"] = 4
	d.offset["missing-cap"] = 5
	d.mu.Unlock()
	if _, err := d.Pick("missing"); err == nil {
		t.Fatal("expected Pick error for missing service")
	}
	if _, err := d.PickByCapability("missing-cap"); err == nil {
		t.Fatal("expected PickByCapability error for missing capability")
	}
	d.mu.Lock()
	_, byName := d.offset["missing"]
	_, byCap := d.offset["missing-cap"]
	d.mu.Unlock()
	if byName {
		t.Fatal("expected stale service offset entry to be removed")
	}
	if byCap {
		t.Fatal("expected stale capability offset entry to be removed")
	}
}
