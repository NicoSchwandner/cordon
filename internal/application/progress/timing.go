package progress

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxHistory      = 10
	defaultEstimate = 120 * time.Second
)

// TimingStore tracks build durations per repo for time estimates.
// Backed by a JSON file that persists across restarts.
type TimingStore struct {
	mu       sync.Mutex
	path     string
	durations map[string][]float64 // repo -> last N durations in seconds
}

// NewTimingStore creates or loads a timing store from disk.
func NewTimingStore(path string) *TimingStore {
	ts := &TimingStore{
		path:      path,
		durations: make(map[string][]float64),
	}
	ts.load()
	return ts
}

// Record saves a build duration for a repo.
func (ts *TimingStore) Record(repo string, d time.Duration) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	secs := d.Seconds()
	history := append(ts.durations[repo], secs)
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}
	ts.durations[repo] = history
	ts.save()
}

// Estimate returns the median build duration for a repo, or a default if no history.
func (ts *TimingStore) Estimate(repo string) time.Duration {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	history := ts.durations[repo]
	if len(history) == 0 {
		return defaultEstimate
	}

	sorted := make([]float64, len(history))
	copy(sorted, history)
	sort.Float64s(sorted)

	median := sorted[len(sorted)/2]
	return time.Duration(median * float64(time.Second))
}

func (ts *TimingStore) load() {
	data, err := os.ReadFile(ts.path)
	if err != nil {
		return // file doesn't exist yet
	}
	if err := json.Unmarshal(data, &ts.durations); err != nil {
		log.Printf("[progress] warning: could not parse timing file: %v", err)
	}
}

func (ts *TimingStore) save() {
	if err := os.MkdirAll(filepath.Dir(ts.path), 0o755); err != nil {
		log.Printf("[progress] warning: could not create timing directory: %v", err)
		return
	}
	data, err := json.MarshalIndent(ts.durations, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(ts.path, data, 0o644); err != nil {
		log.Printf("[progress] warning: could not save timing file: %v", err)
	}
}
