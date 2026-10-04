package lastfm

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
)

// maxQueued bounds the offline queue (Last.fm rejects scrobbles older than
// two weeks anyway).
const maxQueued = 2000

// Queue holds scrobbles that could not be sent yet, persisted to a file so
// they survive restarts.
type Queue struct {
	Path string

	mu    sync.Mutex
	items []Track
}

func LoadQueue(path string) *Queue {
	q := &Queue{Path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &q.items)
	}
	return q
}

func (q *Queue) Add(t Track) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, t)
	if len(q.items) > maxQueued {
		q.items = q.items[len(q.items)-maxQueued:]
	}
	q.save()
}

// Peek returns up to n of the oldest queued scrobbles.
func (q *Queue) Peek(n int) []Track {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n > len(q.items) {
		n = len(q.items)
	}
	return append([]Track(nil), q.items[:n]...)
}

// Drop removes the n oldest scrobbles after they were sent.
func (q *Queue) Drop(n int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n > len(q.items) {
		n = len(q.items)
	}
	q.items = q.items[n:]
	q.save()
}

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *Queue) save() {
	if len(q.items) == 0 {
		if err := os.Remove(q.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return
		}
		return
	}
	data, err := json.Marshal(q.items)
	if err != nil {
		return
	}
	tmp := q.Path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, q.Path)
	}
}
