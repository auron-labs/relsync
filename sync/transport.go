package sync

import (
	"context"
	"sync"
)

// Transport keeps the shared text somewhere every device can reach. It reads
// and writes text and knows nothing of the document in it.
type Transport interface {
	// Read returns the text, and false when nothing has been stored yet.
	Read(ctx context.Context) (text string, found bool, err error)
	// Write replaces the text.
	Write(ctx context.Context, text string) error
}

// Memory keeps the text in memory, for tests; devices that share one Memory
// share a file.
type Memory struct {
	mu     sync.Mutex
	text   string
	found  bool
	reads  int
	writes int
}

var _ Transport = (*Memory)(nil)

// Read returns the stored text.
func (m *Memory) Read(context.Context) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return m.text, m.found, nil
}

// Write stores the text.
func (m *Memory) Write(_ context.Context, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text, m.found = text, true
	m.writes++
	return nil
}

// Text is the stored text, and false when nothing has been written.
func (m *Memory) Text() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.text, m.found
}

// Counts are how many times the text was read and written.
func (m *Memory) Counts() (reads, writes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads, m.writes
}
