package registry

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

const watcherQueueSize = 64

type watcher struct {
	service string
	cb      func(ChangeEvent)
	events  chan ChangeEvent
	mu      sync.Mutex
	closed  bool
	dropped atomic.Int64
}

type watcherPanicReporterFunc func(service string, event ChangeEvent, recovered any, stack []byte)

var watcherPanicReporter atomic.Value

func init() {
	watcherPanicReporter.Store(watcherPanicReporterFunc(defaultWatcherPanicReporter))
}

func defaultWatcherPanicReporter(service string, event ChangeEvent, recovered any, stack []byte) {
	slog.Default().Error(
		"registry watcher callback panicked",
		"service", service,
		"event_type", event.Type,
		"instance_id", event.Instance.ID,
		"panic", fmt.Sprintf("%v", recovered),
		"stack", string(stack),
	)
}

func loadWatcherPanicReporter() watcherPanicReporterFunc {
	reporter, _ := watcherPanicReporter.Load().(watcherPanicReporterFunc)
	return reporter
}

func newWatcher(service string, cb func(ChangeEvent)) *watcher {
	w := &watcher{
		service: service,
		cb:      cb,
		events:  make(chan ChangeEvent, watcherQueueSize),
	}
	go w.loop()
	return w
}

func (w *watcher) loop() {
	for event := range w.events {
		func() {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				reporter := loadWatcherPanicReporter()
				if reporter == nil {
					return
				}
				stack := debug.Stack()
				defer func() {
					_ = recover()
				}()
				reporter(w.service, event, recovered, stack)
			}()
			w.cb(event)
		}()
	}
}

func (w *watcher) enqueue(event ChangeEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	select {
	case w.events <- event:
	default:
		// Drop one stale event to guarantee overflow visibility to consumers.
		select {
		case <-w.events:
		default:
		}
		w.dropped.Add(1)
		select {
		case w.events <- ChangeEvent{Type: ChangeOverflow}:
		default:
		}
	}
}

func (w *watcher) Dropped() int64 {
	return w.dropped.Load()
}

func (w *watcher) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	close(w.events)
}

// Discovery provides utility lookups built on top of registry.
type Discovery struct {
	registry *Registry
	mu       sync.Mutex
	offset   map[string]int
}

// NewDiscovery creates a discovery helper.
func NewDiscovery(registry *Registry) *Discovery {
	return &Discovery{registry: registry, offset: make(map[string]int)}
}

// Pick returns one instance by service name using round-robin.
func (d *Discovery) Pick(name string) (ServiceInstance, error) {
	items := d.registry.Lookup(name)
	if len(items) == 0 {
		d.mu.Lock()
		delete(d.offset, name)
		d.mu.Unlock()
		return ServiceInstance{}, fmt.Errorf("service %q not found", name)
	}
	d.mu.Lock()
	idx := d.offset[name] % len(items)
	d.offset[name] = (idx + 1) % len(items)
	d.mu.Unlock()
	return items[idx], nil
}

// PickByCapability returns one instance by capability using round-robin.
func (d *Discovery) PickByCapability(capability string) (ServiceInstance, error) {
	items := d.registry.LookupByCapability(capability)
	if len(items) == 0 {
		d.mu.Lock()
		delete(d.offset, capability)
		d.mu.Unlock()
		return ServiceInstance{}, fmt.Errorf("capability %q not found", capability)
	}
	d.mu.Lock()
	idx := d.offset[capability] % len(items)
	d.offset[capability] = (idx + 1) % len(items)
	d.mu.Unlock()
	return items[idx], nil
}

// Watch subscribes to a service name.
func (r *Registry) Watch(name string, cb func(ChangeEvent)) (unsubscribe func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watchers[name] == nil {
		r.watchers[name] = make(map[int]*watcher)
	}
	id := r.nextWID
	r.nextWID++
	w := newWatcher(name, cb)
	r.watchers[name][id] = w
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if m := r.watchers[name]; m != nil {
			if watcher, ok := m[id]; ok {
				watcher.close()
				delete(m, id)
			}
			if len(m) == 0 {
				delete(r.watchers, name)
			}
		}
	}
}

func (r *Registry) notify(name string, event ChangeEvent) {
	r.mu.RLock()
	watchers := make([]*watcher, 0, len(r.watchers[name]))
	for _, w := range r.watchers[name] {
		watchers = append(watchers, w)
	}
	r.mu.RUnlock()
	for _, w := range watchers {
		w.enqueue(ChangeEvent{Type: event.Type, Instance: deepCopyInstance(event.Instance)})
	}
}
