package registry

import (
	"errors"
	"sync"
)

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
		r.watchers[name] = make(map[int]func(ChangeEvent))
	}
	id := r.nextWID
	r.nextWID++
	r.watchers[name][id] = cb
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if m := r.watchers[name]; m != nil {
			delete(m, id)
			if len(m) == 0 {
				delete(r.watchers, name)
			}
		}
	}
}

func (r *Registry) notify(name string, event ChangeEvent) {
	r.mu.RLock()
	callbacks := make([]func(ChangeEvent), 0, len(r.watchers[name]))
	for _, cb := range r.watchers[name] {
		callbacks = append(callbacks, cb)
	}
	r.mu.RUnlock()
	for _, cb := range callbacks {
		cb := cb
		go func() {
			defer func() {
				_ = recover()
			}()
			cb(event)
		}()
	}
}
