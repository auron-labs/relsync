package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/backup"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"

	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/oneo-me/onevra/source/components"
)

// applied is what putting a release on the machine did, for the operation to
// record and report.
type applied struct {
	record install.Record
	// kind is what the program turned out to be, for an Item whose kind was
	// guessed; empty otherwise.
	kind   selection.ItemKind
	intent *selection.Intent
	// pathAdded says the tools folder joined the PATH, so a terminal opened
	// before now does not see the commands.
	pathAdded bool
	// warnings are problems that did not undo the install.
	warnings []string
	// shadowed are commands another program earlier on the PATH answers to.
	shadowed []install.Shadow
	// reopen is the program to start again, when the person asked for that.
	reopen string
	// restart says the installer asks for the PC to be restarted to finish.
	restart bool
}

// installContext is everything an install reads about the world, fixed when it
// begins so that a settings change midway does not change it.
type installContext struct {
	entry  catalog.Entry
	rq     installRequest
	plan   plan
	prefs  preferences
	claims catalog.Claims
}

func (s *screen) installContext(e catalog.Entry, rq installRequest, p plan) installContext {
	return installContext{
		entry: e, rq: rq, plan: p, prefs: s.prefs.Get(),
		claims: catalog.ClaimsExcept(s.catalog.Entries().Get(), e.Item.ID),
	}
}

func (s *screen) appsFolder(p preferences) string {
	if p.PortableFolder != "" {
		return p.PortableFolder
	}
	return s.dirs.apps
}

func (s *screen) toolsFolder(p preferences) string {
	if p.ToolsFolder != "" {
		return p.ToolsFolder
	}
	return s.dirs.tools
}

// previousInstall is what the application put on the machine for an Item,
// with its files.
func (s *screen) previousInstall(ctx context.Context, e catalog.Entry) (rec install.Record, ok bool, err error) {
	in, ok := e.Installed()
	if !ok {
		return install.Record{}, false, nil
	}
	files, err := s.catalog.InstallFiles(ctx, e.Item.ID)
	if err != nil {
		return install.Record{}, false, err //nolint:wrapcheck // The catalog says what it was reading.
	}
	return install.Record{
		Method: in.Method, Asset: in.Asset, Folder: in.Folder, MainExe: in.MainExe, Dedicated: in.Dedicated,
		Shortcut: in.Shortcut, SHA256: in.SHA256, Files: files,
		UninstallKey: in.UninstallKey, Package: in.Package, Scope: in.Scope, EntryVersion: in.EntryVersion,
	}, true, nil
}

// portableTarget is the folder a Portable app is installed in. An app that is
// installed stays where it is; otherwise the person's folder is used, or one
// in the apps folder named for the repository, with the owner added when
// another Item has that name.
type portableTarget struct {
	folder    string
	dedicated bool
}

func (s *screen) portableTarget(in installContext, folders platform.Folders, prev *install.Record) (portableTarget, error) {
	if prev != nil && prev.Method == install.MethodPortable {
		return portableTarget{folder: prev.Folder, dedicated: prev.Dedicated}, nil
	}
	item := in.entry.Item
	if item.InstallFolder != "" {
		folder, err := install.CustomFolder(item.InstallFolder, item.Repo, folders.Protected)
		return portableTarget{folder: folder}, err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	taken := func(dir string) bool { _, ok := in.claims.Folders[strings.ToLower(dir)]; return ok }
	return portableTarget{folder: install.DefaultFolder(s.appsFolder(in.prefs), item.Owner, item.Repo, taken), dedicated: true}, nil
}

// applyInstall puts the downloaded release on the machine. It asks what must be
// asked first, and leaves the machine alone until it has everything it needs.
func (s *screen) applyInstall(ctx context.Context, in installContext, file fetch.File, res fetch.Result) (applied, error) {
	e := in.entry
	if res.Verdict == fetch.VerdictAsk || res.Verdict == fetch.VerdictSkip {
		if err := s.askAboutFindings(ctx, in, res); err != nil {
			return applied{}, err
		}
	}
	folders, err := s.platform.Folders()
	if err != nil {
		log.Printf("locate the user's folders: %v", err)
	}
	prevRec, hasPrev, err := s.previousInstall(ctx, e)
	if err != nil {
		return applied{}, err
	}
	var prev *install.Record
	if hasPrev {
		prev = &prevRec
	}

	kind := e.KindFor(in.plan.release)
	artifact, err := settleArtifact(in, file.Path)
	if err != nil {
		return applied{}, err
	}
	if artifact.IsInstaller() {
		return s.applyInstaller(ctx, in, installFile{path: file.Path, kind: artifact}, prev)
	}
	var target portableTarget
	var staged *install.Staged
	defer func() {
		if staged != nil {
			if err := staged.Discard(); err != nil {
				log.Print(err)
			}
		}
	}()
	stage := func(kind selection.ItemKind) (err error) {
		parent := s.dirs.staging
		if kind != selection.ItemTool {
			if target, err = s.portableTarget(in, folders, prev); err != nil {
				return err
			}
			parent = filepath.Dir(target.folder)
		}
		staged, err = install.Stage(ctx, file.Path, in.plan.asset.Name, artifact, parent)
		return err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	if err := stage(kind); err != nil {
		return applied{}, err
	}
	if artifact == selection.ArtifactArchive && kind != selection.ItemTool {
		// An archive that only wraps an installer is a way of delivering it.
		sole, soleKind, ok, err := install.SoleInstaller(staged, e.Item.Method)
		if err != nil {
			return applied{}, err //nolint:wrapcheck // The install package says what it was reading.
		}
		if ok {
			return s.applyInstaller(ctx, in, installFile{path: sole, kind: soleKind}, prev)
		}
	}
	var settled selection.ItemKind
	if e.Item.Kind == "" {
		settled = s.settleKind(staged, e)
		if settled != "" && settled != kind {
			kind = settled
			if err := staged.Discard(); err != nil {
				return applied{}, err //nolint:wrapcheck // The install package says what it was removing.
			}
			staged = nil
			if err := stage(kind); err != nil {
				return applied{}, err
			}
		}
	}

	var done applied
	if kind == selection.ItemTool {
		done, err = s.applyTool(ctx, in, staged, prev)
	} else {
		done, err = s.applyPortable(ctx, in, staged, target, prev, folders)
	}
	if err != nil {
		return applied{}, err
	}
	done.kind = settled
	if prev != nil && prev.Method != done.record.Method {
		if err := s.removeReplaced(ctx, in, *prev, folders); err != nil {
			done.warnings = append(done.warnings, locales.WarningCleanup(err.Error()))
		}
	}
	return done, nil
}

// settleKind reads what the staged release is from its main program: a console
// program is a Tool, a graphical one an App. It is empty when the files do not say.
func (s *screen) settleKind(staged *install.Staged, e catalog.Entry) selection.ItemKind {
	files, err := staged.Files()
	if err != nil {
		return ""
	}
	main, ok := install.MainExecutable(staged.Root, files, e.Item.Repo)
	if !ok {
		return ""
	}
	return install.ExecutableKind(filepath.Join(staged.Root, filepath.FromSlash(main)))
}

// askAboutFindings asks whether to install a download whose checks found
// something the person should weigh.
func (s *screen) askAboutFindings(ctx context.Context, in installContext, res fetch.Result) error {
	warnings := make([]string, len(res.Findings))
	for i, f := range res.Findings {
		warnings[i] = findingText(f, res)
	}
	_, err := s.askOp(ctx, in.rq.itemID, question{
		title: askTitle(in.plan.asset.Name, res), warnings: warnings, confirm: locales.QuestionVerifyConfirm(),
		unattended: background.VerificationFindings,
	})
	return err
}

// closeRunning asks to close the programs running from folder, and closes them.
// When reopenable, the question offers to start the app again afterwards, and
// the result says whether to.
func (s *screen) closeRunning(ctx context.Context, e catalog.Entry, id int64, folder, body, confirm string, reopenable bool) (reopen bool, err error) {
	procs, err := s.platform.ProcessesIn(ctx, folder)
	if err != nil {
		// Not knowing is not a reason to stop: a program that is in the way
		// makes the replacement fail, and that puts the old files back.
		log.Printf("look for programs running from %s: %v", folder, err)
		return false, nil
	}
	if len(procs) == 0 {
		return false, nil
	}
	if s.isUnattended(id) {
		return false, &needsUserError{reason: background.ProgramRunning}
	}
	q := question{title: locales.QuestionRunningTitle(e.Title()), lines: []string{body}, confirm: confirm}
	if reopenable {
		q.tick = locales.QuestionRunningReopen()
	}
	a, err := s.askOp(ctx, id, q)
	if err != nil {
		return false, err
	}
	s.changeOp(id, func(o *operation) { o.stage = stageClosing })
	if err := s.platform.CloseProcesses(ctx, procs); err != nil {
		return false, fmt.Errorf("close %s: %w", e.Title(), err)
	}
	s.changeOp(id, func(o *operation) { o.stage = stageApplying })
	return a.ticked, nil
}

func (s *screen) applyPortable(ctx context.Context, in installContext, staged *install.Staged, target portableTarget, prev *install.Record, folders platform.Folders) (applied, error) {
	e, id := in.entry, in.rq.itemID
	var previous *install.Record
	if prev != nil && prev.Method == install.MethodPortable {
		previous = prev
	}
	reopen := false
	if previous != nil {
		var err error
		if reopen, err = s.closeRunning(ctx, e, id, target.folder, locales.QuestionRunningBody(), locales.QuestionRunningUpdate(), true); err != nil {
			return applied{}, err
		}
		s.backUpBeforeChange(ctx, e, *previous, in.prefs, backup.BeforeUpdate)
		if err := ctx.Err(); err != nil {
			return applied{}, fmt.Errorf("back up %s: %w", e.Title(), err)
		}
	}
	req := install.PortableRequest{
		Repo: e.Item.Repo, Asset: in.plan.asset.Name, Folder: target.folder, Dedicated: target.dedicated, Previous: previous,
		ShortcutText: e.Item.Description, Platform: s.platform,
	}
	if in.prefs.StartMenuShortcuts && folders.StartMenu != "" {
		req.ShortcutPath = filepath.Join(folders.StartMenu, appName, install.SafeName(e.Title())+".lnk")
		if req.ShortcutText == "" {
			req.ShortcutText = e.Title()
		}
	}
	s.changeOp(id, func(o *operation) { o.committing = true })
	out, err := install.ApplyPortable(ctx, staged, req)
	if err != nil {
		return applied{}, err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	done := applied{record: out.Record, intent: intentAfter(e, in.plan.pick, nil)}
	if out.Warning != nil {
		done.warnings = append(done.warnings, locales.WarningShortcut(out.Warning.Error()))
	}
	if reopen && out.Record.MainExe != "" {
		done.reopen = filepath.Join(out.Record.Folder, filepath.FromSlash(out.Record.MainExe))
	}
	return done, nil
}

func (s *screen) applyTool(ctx context.Context, in installContext, staged *install.Staged, prev *install.Record) (applied, error) {
	e, id := in.entry, in.rq.itemID
	members, err := install.ToolMembers(staged, machinePolicy(in.prefs).Target)
	if err != nil {
		return applied{}, err //nolint:wrapcheck // The install package says what it was reading.
	}
	var recorded []string
	if e.Intent != nil {
		recorded = e.Intent.ArchiveMembers
	}
	chosen, err := install.ChooseCommands(members, recorded, e.Item.Repo, in.plan.pick.Product)
	var choice *install.ChoiceError
	if errors.As(err, &choice) {
		chosen, err = s.askPrograms(ctx, in, choice)
	}
	if err != nil {
		return applied{}, err
	}

	var previous *install.Record
	if prev != nil && prev.Method == install.MethodTool {
		previous = prev
	}
	req := install.ToolRequest{
		Dir: s.toolsFolder(in.prefs), Asset: in.plan.asset.Name, Members: chosen, Previous: previous,
		Owners: in.claims.Commands, Replace: map[string]bool{}, Platform: s.platform,
	}
	s.changeOp(id, func(o *operation) { o.committing = true })
	for {
		out, err := install.ApplyTool(ctx, staged, req)
		var foreign *install.ForeignFileError
		if errors.As(err, &foreign) {
			s.changeOp(id, func(o *operation) { o.committing = false })
			_, askErr := s.askOp(ctx, id, question{
				title:   locales.QuestionReplaceTitle(foreign.Command),
				lines:   []string{locales.QuestionReplaceBody(filepath.Base(foreign.Path), e.Title())},
				confirm: locales.QuestionReplaceConfirm(),
			})
			if askErr != nil {
				return applied{}, askErr
			}
			s.changeOp(id, func(o *operation) { o.committing = true })
			req.Replace[strings.ToLower(foreign.Command)] = true
			continue
		}
		if err != nil {
			return applied{}, err //nolint:wrapcheck // The install package's errors are matched by type.
		}
		done := applied{record: out.Record, intent: intentAfter(e, in.plan.pick, chosen), pathAdded: out.PathAdded}
		if out.Warning != nil {
			done.warnings = append(done.warnings, locales.WarningPath(out.Warning.Error()))
		}
		done.shadowed = out.Shadowed
		return done, nil
	}
}

// askPrograms asks which of a Tool's programs to add to the PATH.
func (s *screen) askPrograms(ctx context.Context, in installContext, choice *install.ChoiceError) ([]string, error) {
	q := question{
		title: locales.QuestionProgramsTitle(), lines: []string{locales.QuestionProgramsBody()},
		confirm: locales.QuestionProgramsConfirm(), choices: choice.Candidates, ticked: choice.Kept,
	}
	if len(choice.Missing) > 0 {
		q.lines = []string{locales.QuestionProgramsMissing(in.rq.tag, strings.Join(choice.Missing, ", "))}
	}
	a, err := s.askOp(ctx, in.rq.itemID, q)
	if err != nil {
		return nil, err
	}
	return a.chosen, nil
}

// intentAfter is the choice of file to keep once an install succeeded: the
// file that was installed, so later releases are matched to it, with the
// programs a Tool was given. It is nil when what is kept needs no change.
func intentAfter(e catalog.Entry, pick selection.Candidate, members []string) *selection.Intent {
	switch {
	case e.Intent == nil:
		intent := selection.IntentOf(pick, false)
		intent.ArchiveMembers = members
		return &intent
	case members == nil:
		return nil
	}
	intent := *e.Intent
	intent.ArchiveMembers = slices.Clone(members)
	return &intent
}

// removeReplaced removes an install of another method that an install has
// replaced, so an Item is on the machine once.
func (s *screen) removeReplaced(ctx context.Context, in installContext, rec install.Record, folders platform.Folders) error {
	switch rec.Method {
	case install.MethodInstaller:
		if !rec.Removable() {
			return nil
		}
		return s.removeInstalled(ctx, in.prefs, rec)
	case install.MethodPortable:
		s.backUpBeforeChange(ctx, in.entry, rec, in.prefs, backup.BeforeRemoval)
		res, err := install.RemovePortable(rec, folders.Protected, s.platform)
		return errors.Join(err, res.Warning)
	case install.MethodTool:
		_, err := install.RemoveTool(rec)
		return err //nolint:wrapcheck // The install package says what it was removing.
	}
	return nil
}

// startUninstall removes what the application installed for an Item, and then,
// when forget is set, the Item too. It is queued with the installs.
func (s *screen) startUninstall(e catalog.Entry, forget bool) {
	id := e.Item.ID
	in, managed := e.Installed()
	if !managed {
		if forget {
			s.removeItem(e)
		}
		return
	}
	if _, working := s.opOf(id); working {
		return
	}
	rq := installRequest{itemID: id, tag: in.Tag, kind: opUninstall}
	s.putOp(operation{itemID: id, kind: opUninstall, tag: in.Tag})
	err := s.lanes.Start(laneApply, applyJob(id), func(ctx context.Context, _ jobs.Report) error {
		return s.runUninstall(ctx, rq, forget)
	})
	if err != nil {
		s.takeOp(id)
		if !errors.Is(err, jobs.ErrStopped) {
			log.Print(err)
		}
	}
}

func (s *screen) runUninstall(ctx context.Context, rq installRequest, forget bool) error {
	e, ok := s.catalog.Get(rq.itemID)
	if !ok {
		s.takeOp(rq.itemID)
		return nil
	}
	s.changeOp(rq.itemID, func(o *operation) { o.stage = stageApplying })
	res, err := s.uninstall(ctx, e, rq)
	if err != nil {
		return s.operationFailed(ctx, e, rq, err)
	}
	bg := context.WithoutCancel(ctx)
	if err := s.catalog.ClearInstall(bg, rq.itemID); err != nil {
		s.takeOp(rq.itemID)
		return fmt.Errorf("record removal of %s: %w", e.FullName(), err)
	}
	s.takeOp(rq.itemID)
	s.record(bg, activity.Success, rq.itemID, locales.ActivityUninstalled(e.FullName()))
	if res.Warning != nil {
		s.record(bg, activity.Warning, rq.itemID, locales.ActivityInstallWarning(e.FullName(), locales.WarningShortcut(res.Warning.Error())))
	}
	if res.Left > 0 {
		s.toast(components.ToastSuccess, locales.ToastUninstalledLeft(e.Title(), res.Left))
	} else {
		s.toast(components.ToastSuccess, locales.ToastUninstalled(e.Title()))
	}
	if forget {
		if err := s.catalog.Remove(bg, rq.itemID); err != nil {
			return fmt.Errorf("remove %s: %w", e.FullName(), err)
		}
		s.record(bg, activity.Info, 0, locales.ActivityRemoved(e.FullName()))
	}
	return nil
}

// uninstall removes the files of an install, closing the program first when it
// runs.
func (s *screen) uninstall(ctx context.Context, e catalog.Entry, rq installRequest) (install.RemoveResult, error) {
	rec, ok, err := s.previousInstall(ctx, e)
	if err != nil || !ok {
		return install.RemoveResult{}, err
	}
	folders, err := s.platform.Folders()
	if err != nil {
		log.Printf("locate the user's folders: %v", err)
	}
	if rec.Method == install.MethodPortable || (rec.Method == install.MethodInstaller && rec.Folder != "") {
		if _, err := s.closeRunning(ctx, e, rq.itemID, rec.Folder, locales.QuestionRunningRemoveBody(), locales.QuestionRunningUninstall(), false); err != nil {
			return install.RemoveResult{}, err
		}
	}
	if rec.Method == install.MethodPortable {
		s.backUpBeforeChange(ctx, e, rec, s.prefs.Get(), backup.BeforeRemoval)
		if err := ctx.Err(); err != nil {
			return install.RemoveResult{}, fmt.Errorf("back up %s: %w", e.Title(), err)
		}
	}
	s.changeOp(rq.itemID, func(o *operation) { o.committing = true })
	switch rec.Method {
	case install.MethodTool:
		return install.RemoveTool(rec) //nolint:wrapcheck // The install package says what it was removing.
	case install.MethodInstaller:
		return install.RemoveResult{}, s.removeInstalled(ctx, s.prefs.Get(), rec)
	case install.MethodPortable:
	}
	return install.RemovePortable(rec, folders.Protected, s.platform) //nolint:wrapcheck // The install package says what it was removing.
}

// cleanTools deletes the programs earlier updates and removals left in the tools
// folder because they were running.
func (s *screen) cleanTools() {
	dir := s.toolsFolder(s.prefs.Get())
	s.background(func(context.Context) error {
		install.CleanLeftovers(dir)
		return nil
	})
}
