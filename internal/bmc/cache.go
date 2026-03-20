package bmc

import (
	"sync"
	"time"
)

// CachedResult holds the last BMC poll result.
type CachedResult struct {
	Status  Status
	Sensors SensorData
	Err     error // non-nil if GetStatus failed (BMC unreachable)
}

// Cache deduplicates BMC requests from multiple SSE clients. Only one BMC call
// happens per maxAge interval regardless of how many clients are polling.
type Cache struct {
	client *Client
	maxAge time.Duration

	mu      sync.RWMutex
	result  CachedResult
	updated time.Time
}

// NewCache wraps a BMC client with a time-based cache.
func NewCache(client *Client, maxAge time.Duration) *Cache {
	return &Cache{
		client: client,
		maxAge: maxAge,
	}
}

// Get returns the cached BMC result, refreshing if stale.
func (c *Cache) Get() CachedResult {
	c.mu.RLock()
	if time.Since(c.updated) < c.maxAge {
		r := c.result
		c.mu.RUnlock()
		return r
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if time.Since(c.updated) < c.maxAge {
		return c.result
	}

	c.result.Status, c.result.Err = c.client.GetStatus()
	if c.result.Err == nil {
		c.result.Sensors, _ = c.client.GetSensors()
	} else {
		c.result.Status = Status{PowerState: PowerUnknown}
		c.result.Sensors = SensorData{}
	}
	c.updated = time.Now()
	return c.result
}

// Invalidate forces the next Get to refresh from the BMC.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.updated = time.Time{}
	c.mu.Unlock()
}
