package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/auron-labs/mygo-framework/diagnostics"

	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// openDataFolder reveals the configuration directory in the system file
// manager.
func (s *screen) openDataFolder() {
	if err := s.reveal(s.dirs.config); err != nil {
		log.Printf("open data folder: %v", err)
		s.queueToast(ui.Toast{Title: locales.OpenFolderFailed(), Type: components.ToastError})
	}
}

// saveSupportBundle asks where to save the support bundle and writes it. The
// dialog blocks until it closes, so it runs off the UI thread.
func (s *screen) saveSupportBundle() {
	if s.win == nil {
		return
	}
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: s.win, Title: locales.SaveSupportBundleTitle(), DefaultPath: "support-bundle.zip",
			Filters: []mygo.FileFilter{{Name: "Zip", Extensions: []string{"zip"}}},
		})
		if err != nil || path == "" {
			if err != nil {
				log.Printf("choose support bundle file: %v", err)
			}
			return
		}
		if err := writeSupportBundle(path, s.dirs); err != nil {
			log.Print(err)
			s.win.Update(func() { s.showSupportError() })
			return
		}
		s.win.Update(func() {
			s.queueToast(ui.Toast{Title: locales.SupportBundleSaved(), Type: components.ToastSuccess})
		})
	}()
}

// showSupportError reports a failed bundle write through the dialog host.
func (s *screen) showSupportError() {
	s.showDialog(locales.SaveSupportBundleTitle(), func(c *ui.Context) {
		components.Text(c, locales.SupportBundleFailed()).Wrap()
	})
}

func writeSupportBundle(path string, d dirs) (err error) {
	file, err := os.Create(path) // #nosec G304 -- the user chose this file in the save dialog.
	if err != nil {
		return fmt.Errorf("create support bundle: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if err := diagnostics.Write(context.Background(), file, diagnostics.Options{
		Version:      mygo.App.Version(),
		LogDirectory: filepath.Join(d.cache, logDirectory),
		DatabasePath: d.database(),
		Migrations:   migrations.FS(),
	}); err != nil {
		return fmt.Errorf("write support bundle: %w", err)
	}
	return nil
}
