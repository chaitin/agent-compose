package llms

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// ProbeCacheTTL bounds how long a probe verdict is reused. It is deliberately
// short: an upstream can change what it serves, and the daemon re-probes at
// every startup, so the cache only exists to avoid repeating identical traffic.
const ProbeCacheTTL = 10 * time.Minute

// UpstreamProbeCache remembers the last probe verdict per connection.
//
// It is the in-memory stand-in for persistence: no table owns probe results, so
// they live with the prober that produced them and disappear with the process.
// That is enough for the daemon's own triggers and for a caller that wants the
// most recent verdict without issuing new traffic.
type UpstreamProbeCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]probeCacheEntry
}

type probeCacheEntry struct {
	result   UpstreamProbeResult
	storedAt time.Time
}

// NewUpstreamProbeCache returns a cache that keeps verdicts for ttl. A
// non-positive ttl takes ProbeCacheTTL.
func NewUpstreamProbeCache(ttl time.Duration) *UpstreamProbeCache {
	if ttl <= 0 {
		ttl = ProbeCacheTTL
	}
	return &UpstreamProbeCache{ttl: ttl, now: time.Now, entries: map[string]probeCacheEntry{}}
}

// Lookup returns the cached verdict for key while it is still fresh.
func (c *UpstreamProbeCache) Lookup(key string) (UpstreamProbeResult, bool) {
	if c == nil {
		return UpstreamProbeResult{}, false
	}
	now := c.now
	if now == nil {
		now = time.Now
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return UpstreamProbeResult{}, false
	}
	if now().Sub(entry.storedAt) >= c.ttl {
		delete(c.entries, key)
		return UpstreamProbeResult{}, false
	}
	return entry.result, true
}

// Store records the verdict for key, replacing any earlier one.
func (c *UpstreamProbeCache) Store(key string, result UpstreamProbeResult) {
	if c == nil {
		return
	}
	now := c.now
	if now == nil {
		now = time.Now
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]probeCacheEntry{}
	}
	c.entries[key] = probeCacheEntry{result: result, storedAt: now()}
}

// UpstreamProbeKey identifies a probe target.
//
// It covers the endpoint, the protocol family, and the credential material, so
// a changed base URL, family, header set, or key invalidates the entry on its
// own. The credential is hashed rather than stored, so a cache dump cannot leak
// it. The model is deliberately not part of the key: protocol support does not
// depend on which model a request names.
func UpstreamProbeKey(provider Provider) string {
	material := strings.Join([]string{
		NormalizeProviderType(provider.ProviderType),
		strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/"),
		strings.TrimSpace(provider.AuthHeader),
		strings.TrimSpace(provider.APIKey),
		strings.TrimSpace(provider.HeadersJSON),
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}
