package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const cacheFileName = "cache.json"

// Entry represents a single cached item with expiration
type Entry struct {
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Cache stores cached data with expiration times
type Cache struct {
	Entries map[string]Entry `json:"entries"`
}

// Manager handles cache operations
type Manager struct {
	configDir string
}

// NewManager creates a new cache manager
func NewManager(configDir string) *Manager {
	return &Manager{configDir: configDir}
}

func (m *Manager) cachePath() string {
	return filepath.Join(m.configDir, cacheFileName)
}

func (m *Manager) load() (*Cache, error) {
	data, err := os.ReadFile(m.cachePath())
	if os.IsNotExist(err) {
		return &Cache{Entries: make(map[string]Entry)}, nil
	}
	if err != nil {
		return nil, err
	}

	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		// If cache is corrupted, start fresh
		return &Cache{Entries: make(map[string]Entry)}, nil
	}

	if c.Entries == nil {
		c.Entries = make(map[string]Entry)
	}

	return &c, nil
}

func (m *Manager) save(c *Cache) error {
	if err := os.MkdirAll(m.configDir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.cachePath(), data, 0600)
}

// Get retrieves a cached value if it exists and hasn't expired
func (m *Manager) Get(key string) (string, bool) {
	c, err := m.load()
	if err != nil {
		return "", false
	}

	entry, exists := c.Entries[key]
	if !exists {
		return "", false
	}

	if time.Now().After(entry.ExpiresAt) {
		// Expired - clean it up
		delete(c.Entries, key)
		_ = m.save(c) // Best effort cleanup
		return "", false
	}

	return entry.Value, true
}

// Set stores a value with a TTL duration
func (m *Manager) Set(key, value string, ttl time.Duration) error {
	c, err := m.load()
	if err != nil {
		return err
	}

	c.Entries[key] = Entry{
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}

	return m.save(c)
}

// Delete removes a cached entry
func (m *Manager) Delete(key string) error {
	c, err := m.load()
	if err != nil {
		return err
	}

	delete(c.Entries, key)
	return m.save(c)
}

// IsExpired checks if a key is expired or doesn't exist
func (m *Manager) IsExpired(key string) bool {
	_, valid := m.Get(key)
	return !valid
}
