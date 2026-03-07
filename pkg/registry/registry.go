package registry

import (
	"context"
	"sort"
	"sync"
	"time"
)

const (
	defaultTTL    = 15 * time.Second
	defaultReaper = 1 * time.Second
)

// Registry stores service instances and supports discovery.
type Registry struct {
	nodeID string

	mu       sync.RWMutex
	services map[string]ServiceInstance
	watchers map[string]map[int]*watcher
	nextWID  int

	reaperEvery time.Duration
	ctx         context.Context
	cancel      context.CancelFunc
}

// New creates a registry using default heartbeat expiry settings.
func New(nodeID string) *Registry {
	return NewWithOptions(nodeID, defaultTTL, defaultReaper)
}

// NewWithOptions creates a registry with configurable defaults.
func NewWithOptions(nodeID string, ttl time.Duration, reaperEvery time.Duration) *Registry {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if reaperEvery <= 0 {
		reaperEvery = defaultReaper
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Registry{
		nodeID:      nodeID,
		services:    make(map[string]ServiceInstance),
		watchers:    make(map[string]map[int]*watcher),
		reaperEvery: reaperEvery,
		ctx:         ctx,
		cancel:      cancel,
	}
	go r.reapLoop(ttl)
	return r
}

// NodeID returns the local registry node identifier.
func (r *Registry) NodeID() string {
	return r.nodeID
}

// Close stops internal loops.
func (r *Registry) Close() {
	r.cancel()

	r.mu.Lock()
	watchers := make([]*watcher, 0)
	for _, byID := range r.watchers {
		for _, w := range byID {
			watchers = append(watchers, w)
		}
	}
	r.watchers = make(map[string]map[int]*watcher)
	r.mu.Unlock()

	for _, w := range watchers {
		w.close()
	}
}

// Register creates or updates an instance entry.
func (r *Registry) Register(inst ServiceInstance) {
	if inst.TTL <= 0 {
		inst.TTL = defaultTTL
	}
	if inst.Node == "" {
		inst.Node = r.nodeID
	}
	inst.UpdatedAt = time.Now().UTC()

	r.mu.Lock()
	r.services[inst.ID] = inst
	r.mu.Unlock()

	r.notify(inst.Name, ChangeEvent{Type: ChangeUp, Instance: inst})
}

// Unregister removes an instance.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	inst, ok := r.services[id]
	if ok {
		delete(r.services, id)
	}
	r.mu.Unlock()
	if ok {
		r.notify(inst.Name, ChangeEvent{Type: ChangeDown, Instance: inst})
	}
}

// Heartbeat refreshes ttl on a known service.
func (r *Registry) Heartbeat(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.services[id]
	if !ok {
		return false
	}
	inst.UpdatedAt = time.Now().UTC()
	r.services[id] = inst
	return true
}

// Lookup returns instances by service name.
func (r *Registry) Lookup(name string) []ServiceInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServiceInstance, 0)
	for _, inst := range r.services {
		if inst.Name == name {
			out = append(out, inst)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LookupByCapability returns instances that declare a capability.
func (r *Registry) LookupByCapability(capability string) []ServiceInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServiceInstance, 0)
	for _, inst := range r.services {
		for _, cap := range inst.Capabilities {
			if cap == capability {
				out = append(out, inst)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Snapshot returns all current instances.
func (r *Registry) Snapshot() []ServiceInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServiceInstance, 0, len(r.services))
	for _, inst := range r.services {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MergeSnapshot applies peer instances using last-write-wins by UpdatedAt.
func (r *Registry) MergeSnapshot(instances []ServiceInstance) {
	changes := make([]ChangeEvent, 0)
	r.mu.Lock()
	for _, incoming := range instances {
		current, ok := r.services[incoming.ID]
		if !ok || incoming.UpdatedAt.After(current.UpdatedAt) {
			r.services[incoming.ID] = incoming
			changes = append(changes, ChangeEvent{Type: ChangeUp, Instance: incoming})
		}
	}
	r.mu.Unlock()
	for _, ch := range changes {
		r.notify(ch.Instance.Name, ch)
	}
}

func (r *Registry) reapLoop(defaultTTL time.Duration) {
	ticker := time.NewTicker(r.reaperEvery)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.reapExpired(defaultTTL)
		}
	}
}

func (r *Registry) reapExpired(defaultTTL time.Duration) {
	now := time.Now().UTC()
	expired := make([]ServiceInstance, 0)

	r.mu.Lock()
	for id, inst := range r.services {
		ttl := inst.TTL
		if ttl <= 0 {
			ttl = defaultTTL
		}
		if now.Sub(inst.UpdatedAt) > ttl {
			delete(r.services, id)
			expired = append(expired, inst)
		}
	}
	r.mu.Unlock()

	for _, inst := range expired {
		r.notify(inst.Name, ChangeEvent{Type: ChangeDown, Instance: inst})
	}
}
