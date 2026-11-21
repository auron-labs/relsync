package main

import (
	"context"
	"errors"
	"io"
)

// closers are what the application closes once the event loop has stopped, whether
// it quit from the tray, the window's last close or the system. The order is
// the point: background work stops before the jobs it starts, jobs stop before the
// last preferences are saved because they may still change them, and the
// database closes last because everything before writes to it. A part that was
// never opened is nil and skipped.
type closers struct {
	// background removes the tray icon and stops the update schedule.
	background func()
	jobs       interface{ Shutdown(context.Context) error }
	saver      interface{ Close(context.Context) error }
	database   io.Closer
}

// close closes every part, in order, even when one fails, and returns the
// failures together.
func (c closers) close() error {
	var errs []error
	if c.background != nil {
		c.background()
	}
	if c.jobs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		errs = append(errs, c.jobs.Shutdown(ctx))
		cancel()
	}
	if c.saver != nil {
		ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
		errs = append(errs, c.saver.Close(ctx))
		cancel()
	}
	if c.database != nil {
		errs = append(errs, c.database.Close())
	}
	return errors.Join(errs...)
}
