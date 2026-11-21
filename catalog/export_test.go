package catalog

import "time"

// SetClock replaces the clock the Catalog stamps changes with, so tests can
// simulate devices that edit at different times.
func (c *Catalog) SetClock(now func() time.Time) { c.now = now }
