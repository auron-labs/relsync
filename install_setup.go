package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
)

// installFile is the file an Installer install runs: the download itself, or
// the installer an archive held.
type installFile struct {
	path string
	kind selection.ArtifactKind
}

// settleArtifact is what the downloaded file is, now that its bytes can be
// read: a setup program is inspected for the toolkit it was built with, and one
// that is neither a known toolkit's nor named for setup is a standalone
// program. The Item's install method overrides it.
func settleArtifact(in installContext, path string) (selection.ArtifactKind, error) {
	kind := in.plan.pick.Kind
	if kind != selection.ArtifactSetupExe {
		return kind, nil
	}
	framework, err := install.DetectFramework(path)
	if err != nil {
		return "", err //nolint:wrapcheck // The install package says what it was reading.
	}
	return install.SettleKind(kind, in.entry.Item.Method, framework, in.plan.asset.Name), nil
}

// installerFolder is the folder to ask a setup program to use. An update lands
// where the app already is, unless that folder is gone; otherwise it is the
// folder the person chose, if one.
func (s *screen) installerFolder(in installContext, previous *install.Record) (string, error) {
	if previous != nil && previous.Folder != "" {
		if info, err := os.Stat(previous.Folder); err == nil && info.IsDir() {
			return previous.Folder, nil
		}
	}
	item := in.entry.Item
	if item.InstallFolder == "" {
		return "", nil
	}
	folders, err := s.platform.Folders()
	if err != nil {
		return "", fmt.Errorf("locate the user's folders: %w", err)
	}
	return install.CustomFolder(item.InstallFolder, item.Repo, folders.Protected) //nolint:wrapcheck // The install package's errors are matched by type.
}

// installerLog is where an MSI's verbose log goes.
func (s *screen) installerLog(e catalog.Entry) (string, error) {
	dir := filepath.Join(s.dirs.cache, logDirectory)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create the installer log folder: %w", err)
	}
	return filepath.Join(dir, install.SafeName(e.Item.Repo)+"-install.log"), nil
}

// applyInstaller runs the publisher's installer for the release. An update runs
// it over the installed copy, after the question about the program being open;
// a reinstall removes the installed copy first.
func (s *screen) applyInstaller(ctx context.Context, in installContext, file installFile, prev *install.Record) (applied, error) {
	e, id := in.entry, in.rq.itemID
	var previous *install.Record
	if prev != nil && prev.Method == install.MethodInstaller {
		previous = prev
	}
	if in.rq.unattended {
		if err := unattendedInstallerRefusal(in, file, previous); err != nil {
			return applied{}, err
		}
	}
	folder, err := s.installerFolder(in, previous)
	if err != nil {
		return applied{}, err
	}
	everyone := in.prefs.InstallForEveryone || e.Item.InstallForEveryone
	reopen := false
	if previous != nil {
		if previous.Folder != "" {
			if reopen, err = s.closeRunning(ctx, e, id, previous.Folder, locales.QuestionRunningBody(), locales.QuestionRunningUpdate(), true); err != nil {
				return applied{}, err
			}
		}
		if in.rq.kind == opReinstall {
			if everyone, err = s.reinstallInstalled(ctx, in, file, *previous, everyone); err != nil {
				return applied{}, err
			}
			previous = nil
		}
	}
	req := install.InstallerRequest{
		File: file.path, Asset: in.plan.asset.Name, Kind: file.kind,
		Item:   install.Identity{Owner: e.Item.Owner, Repo: e.Item.Repo, Title: e.Title()},
		Silent: in.prefs.SilentInstalls, Args: e.Item.InstallerArgs, Folder: folder, Everyone: everyone,
		Previous: previous, Platform: s.platform, Timing: s.installTiming, Unattended: in.rq.unattended,
	}
	if file.kind == selection.ArtifactMSI {
		if req.Log, err = s.installerLog(e); err != nil {
			return applied{}, err
		}
	}
	out, err := s.runInstaller(ctx, in, req)
	if err != nil {
		return applied{}, err
	}
	done := applied{record: out.Record, intent: intentAfter(e, in.plan.pick, nil), restart: out.RestartNeeded}
	if out.Warning != nil {
		done.warnings = append(done.warnings, locales.WarningRecord(out.Warning.Error()))
	}
	if reopen && out.Record.MainExe != "" {
		done.reopen = filepath.Join(out.Record.Folder, filepath.FromSlash(out.Record.MainExe))
	}
	return done, nil
}

// unattendedInstallerRefusal is the reason an installer may not run with nobody
// watching, nil when it may: it updates a copy installed for the current user,
// so raises no administrator prompt, and has a way to run without its window.
func unattendedInstallerRefusal(in installContext, file installFile, previous *install.Record) error {
	if previous == nil || previous.Scope != platform.ScopeUser {
		return &needsUserError{reason: background.NeedsElevation}
	}
	framework := install.FrameworkUnknown
	if file.kind == selection.ArtifactSetupExe {
		var err error
		if framework, err = install.DetectFramework(file.path); err != nil {
			return err //nolint:wrapcheck // The install package says what it was reading.
		}
	}
	if !background.RunsQuietly(file.kind, framework, in.entry.Item.InstallerArgs) {
		return &needsUserError{reason: background.NeedsInterface}
	}
	return nil
}

// runInstaller runs the installer, and runs it again once the person has
// accepted a licence it asked for. The machine is being changed while it runs.
func (s *screen) runInstaller(ctx context.Context, in installContext, req install.InstallerRequest) (install.Outcome, error) {
	id := in.rq.itemID
	s.changeOp(id, func(o *operation) { o.committing = true })
	for {
		out, err := install.ApplyInstaller(ctx, req)
		var license *install.LicenseRequiredError
		if !errors.As(err, &license) {
			return out, err //nolint:wrapcheck // The install package's errors are matched by type.
		}
		s.changeOp(id, func(o *operation) { o.committing = false })
		args, err := s.acceptLicense(ctx, in, license.Setting)
		if err != nil {
			return install.Outcome{}, err
		}
		req.Args = args
		s.changeOp(id, func(o *operation) { o.committing = true })
	}
}

// acceptLicense asks whether to accept the licence an installer asked for. On
// yes the setting is added to the Item's installer arguments, which the
// answer is kept in, and the arguments are returned. Nothing is ever added
// without the answer.
func (s *screen) acceptLicense(ctx context.Context, in installContext, setting string) (string, error) {
	e := in.entry
	if _, err := s.askOp(ctx, in.rq.itemID, question{
		title:   locales.QuestionLicenseTitle(e.Title()),
		lines:   []string{locales.QuestionLicenseBody(e.Title(), setting)},
		confirm: locales.QuestionLicenseConfirm(), unattended: background.LicenseQuestion,
	}); err != nil {
		return "", err
	}
	args := strings.TrimSpace(e.Item.InstallerArgs + " " + setting)
	if err := s.catalog.SetInstallerArgs(context.WithoutCancel(ctx), e.Item.ID, args); err != nil {
		return "", fmt.Errorf("keep the license answer for %s: %w", e.FullName(), err)
	}
	s.record(context.WithoutCancel(ctx), activity.Info, e.Item.ID, locales.ActivityLicenseAccepted(e.FullName(), setting))
	return args, nil
}

// reinstallInstalled asks to reinstall, then uninstalls the installed copy so
// that the same release can be installed again. For an MSI the question offers
// to install for everyone; its answer is returned.
func (s *screen) reinstallInstalled(ctx context.Context, in installContext, file installFile, previous install.Record, everyone bool) (bool, error) {
	e := in.entry
	q := question{
		title: locales.QuestionReinstallTitle(e.Title()), lines: []string{locales.QuestionReinstallBody(e.Title(), in.rq.tag)},
		confirm: locales.QuestionReinstallConfirm(),
	}
	if file.kind == selection.ArtifactMSI {
		q.tick, q.tickOff = locales.QuestionEveryone(), !everyone
	}
	a, err := s.askOp(ctx, in.rq.itemID, q)
	if err != nil {
		return false, err
	}
	if file.kind == selection.ArtifactMSI {
		everyone = a.ticked
	}
	s.changeOp(in.rq.itemID, func(o *operation) { o.committing = true })
	if previous.Removable() {
		if err := s.removeInstalled(ctx, in.prefs, previous); err != nil {
			return false, err
		}
	}
	return everyone, nil
}

// removeInstalled runs the installed program's own uninstaller.
func (s *screen) removeInstalled(ctx context.Context, prefs preferences, rec install.Record) error {
	return install.RemoveInstalled(ctx, install.RemoveRequest{ //nolint:wrapcheck // The install package's errors are matched by type.
		Record: rec, Silent: prefs.SilentInstalls, Platform: s.platform, Timing: s.installTiming,
	})
}
