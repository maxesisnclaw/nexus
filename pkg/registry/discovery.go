package registry

import (
	"errors"
	"sync"
)

const watcherQueueSize = 64

type watcher struct {
	cb     func(ChangeEvent)
	events chan ChangeEvent
	mu     sync.Mutex
	closed bool
}

func newWatcher(cb func(ChangeEvent)) *watcher {
	w := &watcher{
		cb:     cb,
		events: make(chan ChangeEvent, watcherQueueSize),
	}
	go w.loop()
	return w
}

func (w *watcher) loop() {
	for event := range w.events {
		func() {
			defer func() {
				_ = recover()
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
	}
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
		return ServiceInstance{}, errors.New("service not found")
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
		return ServiceInstance{}, errors.New("capability not found")
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
	w := newWatcher(cb)
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
		w.enqueue(event)
	}
}
