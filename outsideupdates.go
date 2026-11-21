package main

import (
	"context"
	"log"
	"path/filepath"
	"regexp"

	"github.com/auron-labs/mygo-framework/errorreport"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"
)

// dottedVersion is a version the system's list can be trusted to compare: two
// or more numbers.
var dottedVersion = regexp.MustCompile(`\d+\.\d+`)

// checkOutsideUpdates looks for installer-based apps that were updated by
// something other than this application, once a check has found out which
// releases exist. A failure is reported here: nothing else is waiting for it.
func (s *screen) checkOutsideUpdates(ctx context.Context) {
	if err := s.syncOutsideUpdates(ctx); err != nil && ctx.Err() == nil {
		log.Printf("look for updates made outside: %v", err)
		errorreport.Capture(err, "outside.updates", "jobs")
	}
}

// syncOutsideUpdates moves the installed version of each installer-based app to
// the newer release its entry in the list of installed programs now reports, or
// when the entry has no usable version, its program's product version does.
func (s *screen) syncOutsideUpdates(ctx context.Context) error {
	var listed []catalog.Entry
	for _, e := range s.catalog.Entries().Get() {
		if in, ok := e.Installed(); ok && in.Method == install.MethodInstaller && in.UninstallKey != "" {
			if _, working := s.opOf(e.Item.ID); !working {
				listed = append(listed, e)
			}
		}
	}
	if len(listed) == 0 {
		return nil
	}
	programs, err := s.platform.Programs(ctx)
	if err != nil {
		return err //nolint:wrapcheck // The platform says what it was reading.
	}
	for _, e := range listed {
		in, _ := e.Installed()
		entry, ok := install.Entry(programs, in.UninstallKey)
		if !ok {
			continue
		}
		seen := entry.Version
		release, found := e.ReleaseForVersion(seen)
		if !found && !dottedVersion.MatchString(seen) && in.MainExe != "" {
			// The entry says nothing a version can be read from; the program may.
			program := filepath.Join(in.Folder, filepath.FromSlash(in.MainExe))
			if seen, err = s.platform.FileVersion(program); err != nil {
				log.Printf("read the version of %s: %v", program, err)
				continue
			}
			release, found = e.ReleaseForVersion(seen)
		}
		if found {
			s.moveToRelease(ctx, e, release, seen)
		}
	}
	return nil
}

// moveToRelease records that an app is now at a release, as found outside.
func (s *screen) moveToRelease(ctx context.Context, e catalog.Entry, release source.Release, seen string) {
	if err := s.catalog.MoveInstalled(ctx, e.Item.ID, release.Tag, seen); err != nil {
		if ctx.Err() == nil {
			log.Printf("record the update of %s made outside: %v", e.FullName(), err)
			errorreport.Capture(err, "outside.updates", "jobs")
		}
		return
	}
	s.record(ctx, activity.Info, e.Item.ID, locales.ActivityUpdatedOutside(e.FullName(), appName))
}
