package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// pingCache persists the most recent PingResult per server fingerprint so the
// UI can render last-known latencies after a page reload (or even a manager
// restart) without re-running the full test.
//
// Two locks: `mu` protects the in-memory map; `saveMu` serialises atomic
// tmp+rename writes so concurrent Update calls don't race on the temp file.
type pingCache struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	byID   map[string]PingResult
	mkdir  sync.Once
}

func newPingCache(path string) *pingCache {
	pc := &pingCache{path: path, byID: map[string]PingResult{}}
	pc.load()
	return pc
}

func (c *pingCache) load() {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = json.Unmarshal(data, &c.byID)
}

func (c *pingCache) save() {
	c.mu.RLock()
	data, err := json.Marshal(c.byID)
	c.mu.RUnlock()
	if err != nil {
		return
	}
	c.mkdir.Do(func() { _ = os.MkdirAll(filepath.Dir(c.path), 0755) })

	// Serialise tmp+rename so two concurrent writers don't lose data.
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		_ = os.Rename(tmp, c.path)
	}
}

// Update replaces the cached entries for every server in results and writes
// the new state to disk.
func (c *pingCache) Update(results []PingResult) {
	c.mu.Lock()
	for _, r := range results {
		c.byID[r.ServerID] = r
	}
	c.mu.Unlock()
	c.save()
}

// Set updates one in-memory result while a long ping cycle is still running.
// The completed batch is persisted by Update, avoiding a flash write per host.
func (c *pingCache) Set(result PingResult) {
	c.mu.Lock()
	c.byID[result.ServerID] = result
	c.mu.Unlock()
}

// MigrateEquivalent carries diagnostic history across subscription credential
// rotation. Providers may change UUID, Reality keys or other authentication
// fields on every refresh, which changes the server fingerprint even though
// the visible endpoint is still the same. The copied result remains only UI
// history; start and failover selection always perform a fresh probe.
func (c *pingCache) MigrateEquivalent(previous, fresh []VLESSServer) int {
	c.mu.Lock()
	migrated := 0
	for _, candidate := range fresh {
		if _, exists := c.byID[candidate.ID]; exists {
			continue
		}
		for _, old := range previous {
			if !sameCatalogServer(old, candidate) {
				continue
			}
			result, exists := c.byID[old.ID]
			if !exists {
				break
			}
			result.ServerID = candidate.ID
			result.ServerName = candidate.Name
			result.Address = candidate.Address
			result.Port = candidate.Port
			result.Protocol = describeProtocol(&candidate)
			c.byID[candidate.ID] = result
			migrated++
			break
		}
	}
	c.mu.Unlock()
	if migrated > 0 {
		c.save()
	}
	return migrated
}

// All returns a snapshot of every cached result.
func (c *pingCache) All() []PingResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]PingResult, 0, len(c.byID))
	for _, r := range c.byID {
		out = append(out, r)
	}
	return out
}

func (c *pingCache) Get(id string) (PingResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result, ok := c.byID[id]
	return result, ok
}

// Prune drops cache entries whose server ID isn't in the given keep set —
// called after subscription refresh to avoid accumulating stale entries.
func (c *pingCache) Prune(keepIDs map[string]bool) {
	c.mu.Lock()
	changed := false
	for id := range c.byID {
		if !keepIDs[id] {
			delete(c.byID, id)
			changed = true
		}
	}
	c.mu.Unlock()
	if changed {
		c.save()
	}
}
