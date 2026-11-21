package imagecache

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := range 4 {
		for y := range 4 {
			img.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func waitReady(t *testing.T, c *Cache, url string, arrived <-chan struct{}) {
	t.Helper()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the image never arrived")
	}
	if c.Get(url) == nil {
		t.Fatal("notified, but the image is not available")
	}
}

func TestGetReturnsNilThenTheImageAndNotifiesOnArrival(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(pngBytes(t))
	}))
	t.Cleanup(server.Close)
	arrived := make(chan struct{}, 4)
	c := New(t.TempDir(), nil, func() { arrived <- struct{}{} })
	t.Cleanup(c.Close)

	url := server.URL + "/avatar"
	if c.Get(url) != nil {
		t.Fatal("an image that was never loaded is available")
	}
	waitReady(t, c, url, arrived)
	c.Get(url)
	c.Get(url)
	if got := hits.Load(); got != 1 {
		t.Errorf("%d downloads for one URL", got)
	}
	if c.Get("") != nil {
		t.Error("an empty URL returned an image")
	}
}

func TestImagesComeFromDiskWhenTheServerIsGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(pngBytes(t)) }))
	dir := t.TempDir()
	url := server.URL + "/a"
	arrived := make(chan struct{}, 4)
	first := New(dir, nil, func() { arrived <- struct{}{} })
	first.Get(url)
	waitReady(t, first, url, arrived)
	first.Close()
	server.Close()

	second := New(dir, nil, func() { arrived <- struct{}{} })
	t.Cleanup(second.Close)
	second.Get(url)
	waitReady(t, second, url, arrived)
}

func TestAStaleFileIsUsedWhenTheServerFails(t *testing.T) {
	dir := t.TempDir()
	url := "http://127.0.0.1:1/unreachable"
	if err := writeFile(dir, dir+"/"+fileName(url), pngBytes(t)); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * freshFor)
	if err := os.Chtimes(dir+"/"+fileName(url), old, old); err != nil {
		t.Fatal(err)
	}
	arrived := make(chan struct{}, 1)
	c := New(dir, nil, func() { arrived <- struct{}{} })
	t.Cleanup(c.Close)
	c.Get(url)
	waitReady(t, c, url, arrived)
}

func TestFailuresStayNilAndAreNotRetried(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("not an image"))
	}))
	t.Cleanup(server.Close)
	c := New(t.TempDir(), nil, nil)
	for _, path := range []string{"/missing", "/garbage"} {
		c.Get(server.URL + path)
	}
	c.wg.Wait() // both loads have ended
	for range 3 {
		if c.Get(server.URL+"/missing") != nil || c.Get(server.URL+"/garbage") != nil {
			t.Fatal("a failed image became available")
		}
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("%d requests, want one per URL", got)
	}
}

func TestMemoryIsBoundedAndEvictedImagesReload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(pngBytes(t)) }))
	t.Cleanup(server.Close)
	arrived := make(chan struct{}, maxDecoded*2)
	c := New(t.TempDir(), nil, func() { arrived <- struct{}{} })
	t.Cleanup(c.Close)
	for i := range maxDecoded + 20 {
		c.Get(server.URL + "/" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	for range maxDecoded + 20 {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("images did not all load")
		}
	}
	c.mu.Lock()
	decoded := c.ready.Len()
	c.mu.Unlock()
	if decoded != maxDecoded {
		t.Errorf("%d decoded images kept, want %d", decoded, maxDecoded)
	}
}
