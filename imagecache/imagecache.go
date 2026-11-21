// Package imagecache loads remote images, such as avatars, for views. A view
// asks for an image by URL and gets it once it is ready; fetching, disk
// caching and decoding happen on other goroutines, never in the view.
package imagecache

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	// maxDecoded bounds the decoded images kept in memory; evicted ones reload
	// from disk when asked for again.
	maxDecoded = 256
	// maxFetches bounds the downloads in flight.
	maxFetches = 4
	// maxImageBytes bounds one downloaded image.
	maxImageBytes = 4 << 20
	// freshFor is how long a cached file is used without asking the server.
	freshFor = 7 * 24 * time.Hour
)

// Cache is a disk-backed, memory-bounded image cache. It is safe for
// concurrent use.
type Cache struct {
	dir    string
	client *http.Client
	ctx    context.Context //nolint:containedctx // The cache's lifetime context parents every background load.
	cancel context.CancelFunc
	wg     sync.WaitGroup
	slots  chan struct{}

	mu      sync.Mutex
	notify  func()
	entries map[string]*entry
	// ready lists the decoded entries, most recently used first.
	ready *list.List
}

type entry struct {
	bitmap *ui.Bitmap // nil while loading or after a failure
	// pos is the entry's place in ready, nil unless decoded.
	pos *list.Element
	url string
}

// New returns a cache storing files in dir. notify is called, from another
// goroutine, whenever an image becomes available, so the window can redraw; it
// may be nil and replaced with SetNotify. A nil client uses a default with a
// timeout.
func New(dir string, client *http.Client, notify func()) *Cache {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Cache{
		dir: dir, client: client, notify: notify, ctx: ctx, cancel: cancel,
		slots: make(chan struct{}, maxFetches), entries: map[string]*entry{}, ready: list.New(),
	}
}

// SetNotify replaces the function called when an image becomes available, for
// a window that opens after the cache.
func (c *Cache) SetNotify(notify func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notify = notify
}

// Get returns the decoded image for url, or nil while it is not available: not
// asked for yet, still loading, or failed. The first call for a URL starts
// loading it. It only reads memory, so views may call it every frame.
func (c *Cache) Get(url string) ui.ImageSource {
	if url == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[url]; ok {
		if e.bitmap == nil {
			return nil
		}
		c.ready.MoveToFront(e.pos)
		return e.bitmap
	}
	c.entries[url] = &entry{url: url}
	c.wg.Add(1)
	go c.load(url)
	return nil
}

// Close stops loading and waits for loads in progress to end.
func (c *Cache) Close() {
	c.cancel()
	c.wg.Wait()
}

func (c *Cache) load(url string) {
	defer c.wg.Done()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-c.ctx.Done():
		return
	}
	bitmap, err := c.fetch(url)
	c.mu.Lock()
	if err != nil {
		// Left as a failed entry, so a broken URL is not retried on every
		// frame. The next session tries again.
		c.mu.Unlock()
		return
	}
	e := c.entries[url]
	e.bitmap = bitmap
	e.pos = c.ready.PushFront(e)
	for c.ready.Len() > maxDecoded {
		oldest := c.ready.Back()
		evicted := c.ready.Remove(oldest).(*entry)
		delete(c.entries, evicted.url)
	}
	notify := c.notify
	c.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// fetch returns the image from the disk cache when it is fresh, otherwise from
// the network, falling back to a stale file when the network fails.
func (c *Cache) fetch(url string) (*ui.Bitmap, error) {
	path := filepath.Join(c.dir, fileName(url))
	info, statErr := os.Stat(path)
	stale := statErr == nil
	if stale && time.Since(info.ModTime()) < freshFor {
		if bitmap, err := decodeFile(path); err == nil {
			return bitmap, nil
		}
	}
	data, err := c.download(url)
	if err != nil {
		if stale {
			if bitmap, decodeErr := decodeFile(path); decodeErr == nil {
				return bitmap, nil
			}
		}
		return nil, err
	}
	bitmap, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil, fmt.Errorf("decode image %s: %w", url, err)
	}
	// A failed write only costs a download next time.
	_ = writeFile(c.dir, path, data)
	return bitmap, nil
}

func (c *Cache) download(url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build image request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download image %s: %w", url, err)
	}
	if len(data) > maxImageBytes {
		return nil, errors.New("download image " + url + ": too large")
	}
	return data, nil
}

func decodeFile(path string) (*ui.Bitmap, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path is inside the cache directory, named by a hash.
	if err != nil {
		return nil, fmt.Errorf("read cached image: %w", err)
	}
	bitmap, err := ui.DecodeBitmap(data)
	if err != nil {
		// A damaged file would fail forever; drop it so the next load downloads again.
		_ = os.Remove(path)
		return nil, fmt.Errorf("decode cached image: %w", err)
	}
	return bitmap, nil
}

// writeFile stores data atomically, so a reader never sees half an image.
func writeFile(dir, path string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create image cache: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return fmt.Errorf("create image cache file: %w", err)
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write image cache file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("store image cache file: %w", err)
	}
	return nil
}

func fileName(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}
