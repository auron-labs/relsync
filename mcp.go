package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/auron-labs/mygo-framework/database"
	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/secrets/memory"
	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/database/migrations"

	mygomcp "github.com/auron-labs/mygo-mcp"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/theme"
)

// nativeFixture is the application's real view for the MCP harness, with
// independent state: its own throwaway database and credential store, and
// preferences that are neither loaded nor saved.
func nativeFixture() mygomcp.Fixture {
	return mygomcp.Fixture{
		Name: appName, Width: windowWidth, Height: windowHeight,
		Factory: func() (func(*ui.Context), func(), error) {
			dir, err := os.MkdirTemp("", appSlug+"-mcp-*")
			if err != nil {
				return nil, nil, fmt.Errorf("create fixture directory: %w", err)
			}
			db, err := database.Open(context.Background(), filepath.Join(dir, "app.db"), migrations.FS())
			if err != nil {
				return nil, nil, errors.Join(fmt.Errorf("open fixture database: %w", err), os.RemoveAll(dir))
			}
			d, err := resolveDirs(dir)
			if err != nil {
				return nil, nil, errors.Join(err, db.Close(), os.RemoveAll(dir))
			}
			manager := jobs.New(context.Background())
			svc, err := newServices(context.Background(), db.SQL, d, manager, memory.New())
			if err != nil {
				return nil, nil, errors.Join(err, db.Close(), os.RemoveAll(dir))
			}
			cell := state.NewCell(defaultPreferences())
			saver := state.Autosave(cell, saveDelay, func(context.Context, preferences) error { return nil })
			s := newScreen(cell, saver, manager, d, svc)
			cleanup := func() {
				_ = saver.Close(context.Background())
				_ = manager.Shutdown(context.Background())
				svc.avatars.Close()
				_ = db.Close()
				_ = os.RemoveAll(dir)
			}
			return func(c *ui.Context) { theme.Apply(c); s.view(c) }, cleanup, nil
		},
	}
}
