package background

import "time"

// Manual is the check interval that never schedules a check.
const Manual = "manual"

// IntervalOption is one choice of how often to check for updates.
type IntervalOption struct {
	// Value is what the setting stores.
	Value string
	// Every is the time between checks.
	Every time.Duration
}

// Intervals are the choices of how often to check, shortest first, ending with
// Manual.
var Intervals = []IntervalOption{
	{"15m", 15 * time.Minute},
	{"30m", 30 * time.Minute},
	{"1h", time.Hour},
	{"3h", 3 * time.Hour},
	{"6h", 6 * time.Hour},
	{"12h", 12 * time.Hour},
	{"24h", 24 * time.Hour},
	{Manual, 0},
}

// DefaultInterval is the choice of a fresh installation.
const DefaultInterval = "6h"

// IntervalEvery is the time between checks for a stored choice, zero for
// Manual or a value that is not one of Intervals.
func IntervalEvery(value string) time.Duration {
	for _, o := range Intervals {
		if o.Value == value {
			return o.Every
		}
	}
	return 0
}

// BackupOff is the backup interval that never schedules a backup.
const BackupOff = "off"

// BackupIntervals are the choices of how often to back the Library up, ending
// with BackupOff. A month is 30 days.
var BackupIntervals = []IntervalOption{
	{"6h", 6 * time.Hour},
	{"12h", 12 * time.Hour},
	{"1d", 24 * time.Hour},
	{"3d", 3 * 24 * time.Hour},
	{"7d", 7 * 24 * time.Hour},
	{"14d", 14 * 24 * time.Hour},
	{"30d", 30 * 24 * time.Hour},
	{BackupOff, 0},
}

// BackupEvery is the time between backups for a stored choice, zero for
// BackupOff or a value that is not one of BackupIntervals.
func BackupEvery(value string) time.Duration {
	for _, o := range BackupIntervals {
		if o.Value == value {
			return o.Every
		}
	}
	return 0
}
