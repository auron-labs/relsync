package catalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"
)

func newManager(t *testing.T) *jobs.Manager {
	t.Helper()
	m := jobs.New(t.Context())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
