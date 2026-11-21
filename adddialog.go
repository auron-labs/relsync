package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/hints"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// addState is the add dialog: a repository being looked up, then previewed.
// It is only touched on the UI thread.
type addState struct {
	ref     source.RepoRef
	loading bool
	// failure is why the lookup failed, for the user.
	failure string
	preview *catalog.Preview

	includePrereleases bool
	alreadyHave        bool
	version            string
	// found is the entry of the system's installed programs that looks like the
	// repository, if there is one. located is a program file the person pointed
	// at instead; it takes the place of found.
	found   *platform.Program
	located string
	// tag is the release whose files are shown; empty is the newest installable.
	tag string
	// file is the name of the file the user picked; empty takes the best match.
	file string
	// kind is what the user chose the Item to be; empty detects it.
	kind selection.ItemKind

	// groupID is the group the person chose for the Item; 0 leaves it in its
	// category's.
	groupID int64
	// hints are the signals shown under "At a glance"; empty until they are known.
	hints []hints.Hint
	// installAfter installs the release as soon as the repository is tracked.
	installAfter bool
	// folder is where the user asked a Portable app to be installed; empty is
	// the default place. folderProblem is why a chosen folder was refused.
	folder        string
	folderProblem string
}

// submitOmnibox acts on the top box: a repository reference starts adding it;
// a web address opens in the browser; any other text is searched for in Discover.
func (s *screen) submitOmnibox() {
	text := strings.TrimSpace(s.omnibox)
	if text == "" {
		return
	}
	if ref, ok := s.sources.ParseLink(text); ok {
		s.omnibox = ""
		s.startAdd(ref)
		return
	}
	if isWebAddress(text) {
		if err := s.openURL(text); err != nil {
			log.Printf("open %s: %v", text, err)
		}
		return
	}
	s.startSearch(text)
}

func isWebAddress(text string) bool {
	u, err := url.Parse(text)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// startAdd previews a repository in a dialog, or selects it when it is already
// tracked.
func (s *screen) startAdd(ref source.RepoRef) {
	if existing, ok := s.catalog.Find(ref); ok {
		s.queueToast(ui.Toast{Title: locales.ToastAlreadyTracked(existing.Title()), Type: components.ToastInfo})
		s.navigate("library")
		s.openItem(existing.Item.ID)
		return
	}
	prefs := s.prefs.Get()
	st := &addState{ref: ref, loading: true, includePrereleases: prefs.DefaultIncludePrereleases, installAfter: prefs.InstallAfterAdding}
	s.add = st
	s.showDialog(locales.AddTitle(), func(c *ui.Context) { s.addDialog(c, st) })
	s.background(func(ctx context.Context) error {
		preview, err := s.catalog.Preview(ctx, ref)
		if ctx.Err() != nil {
			return fmt.Errorf("preview %s: %w", ref, ctx.Err())
		}
		var found *platform.Program
		var glance []hints.Hint
		if err == nil {
			found = s.findOnPC(ctx, preview.Repository)
			var latest *source.Release
			if len(preview.Page.Releases) > 0 {
				latest = &preview.Page.Releases[0]
			}
			glance = s.safetyHints(ctx, preview.Repository, latest)
		}
		s.onUI(func() {
			if s.add != st {
				return
			}
			st.loading = false
			if err != nil {
				// A repository that cannot be found or reached is shown, not reported.
				st.failure = errorText(err)
				return
			}
			st.preview, st.hints = &preview, glance
			if found != nil {
				st.found, st.alreadyHave, st.version = found, true, found.Version
			}
		})
		return nil
	})
}

// confidentMatch is the lowest score at which an entry of the installed programs
// is taken for the repository's program.
const confidentMatch = 65

// findOnPC finds the entry of the system's installed programs that best matches
// the repository, when one does confidently and the Library does not hold it.
// A list that cannot be read has no match.
func (s *screen) findOnPC(ctx context.Context, repo source.Repository) *platform.Program {
	programs, err := s.platform.Programs(ctx)
	if err != nil {
		if !errors.Is(err, platform.ErrNotSupported) && ctx.Err() == nil {
			log.Printf("list the installed programs: %v", err)
		}
		return nil
	}
	held := onPC(s.catalog.Entries().Get()).Keys
	id := install.Identity{Owner: repo.Owner, Repo: repo.Name, Title: repo.Name}
	var best *platform.Program
	bestScore := confidentMatch - 1
	for _, p := range programs {
		if score := install.MatchScore(p, id); score > bestScore && !held[p.Key] {
			best, bestScore = &p, score
		}
	}
	return best
}

func (s *screen) addDialog(c *ui.Context, st *addState) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(3)).MinWidth(400).MaxWidth(520).Children(func() {
		components.Text(c, locales.AddTitle()).Bold().FontSize(t.Rem(1.1))
		switch {
		case st.loading:
			components.Text(c, locales.AddLookingUp(st.ref.String())).TextColor(t.TextMuted)
		case st.failure != "":
			components.Text(c, st.failure).TextColor(t.Danger).Wrap()
			ui.Row(c).Justify(ui.End).Children(func() {
				if components.Button(c, locales.Close(), components.ButtonSecondary).Clicked() {
					s.dialogOpen = false
				}
			})
		default:
			s.addPreview(c, st)
		}
	})
}

// options are the choices made so far in the dialog, apart from the file.
func (st *addState) options() catalog.AddOptions {
	return catalog.AddOptions{
		IncludePrereleases: st.includePrereleases, Kind: st.kind, AlreadyHave: st.alreadyHave, InstalledVersion: st.version,
		InstallFolder: st.folder, GroupID: st.groupID,
	}
}

// shown is the release whose files the dialog lists: the one picked, or the
// newest with a download.
func (st *addState) shown(draft catalog.Entry) (source.Release, bool) {
	if i := slices.IndexFunc(draft.Versions(), func(r source.Release) bool { return r.Tag == st.tag }); i >= 0 {
		return draft.Versions()[i], true
	}
	return draft.Offered()
}

func (s *screen) addPreview(c *ui.Context, st *addState) {
	t := c.Theme()
	draft := st.preview.Draft(st.options())
	repo := st.preview.Repository
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		s.avatar(c, draft, 48)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			components.Text(c, repo.Name).Bold().FontSize(t.Rem(1.1)).Wrap()
			components.Text(c, repo.Owner).TextColor(t.TextMuted).Wrap()
		})
	})
	if repo.Description != "" {
		components.Text(c, repo.Description).Wrap()
	}
	latest := locales.AddNoReleases()
	if r, ok := draft.Offered(); ok {
		latest = locales.AddLatestVersion(r.Tag)
	}
	ui.Row(c).FillWidth().Gap(t.Space(4)).Wrap().Children(func() {
		components.Text(c, locales.AddStars(repo.Stars)).TextColor(t.TextMuted)
		components.Text(c, latest).TextColor(t.TextMuted)
	})
	s.atAGlance(c, st.hints)
	view, shown := s.addDownload(c, st, draft)
	components.Checkbox(c, &st.includePrereleases, locales.AddIncludePrereleases())
	labelled(c, locales.AddGroup(), func() {
		choice, changed := dropdown(c, locales.DrawerGroupLabel(), groupValue(st.groupID), s.groupOptions())
		if changed {
			st.groupID = parseGroupValue(choice)
		}
	})
	s.addOnPC(c, st)
	if st.alreadyHave {
		components.TextBox(c, &st.version, components.TextBoxOptions{}).
			Label(locales.AddVersionLabel()).Placeholder(locales.AddVersionPlaceholder()).FillWidth()
	} else if shown && view.hasPick {
		components.Checkbox(c, &st.installAfter, locales.AddInstallAfter())
	}
	if shown && view.hasPick && draft.KindFor(view.release) != selection.ItemTool {
		s.addLocation(c, st, view.pick.Kind.IsInstaller())
	}
	ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
		if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
			s.dialogOpen = false
		}
		if components.Button(c, locales.AddConfirm(), components.ButtonPrimary).Clicked() {
			s.dialogOpen = false
			s.confirmAdd(st, view, shown)
		}
	})
}

// addOnPC is the dialog's "Already on this PC?": what was found in the installed
// programs, a way to point at the program instead, and the box that says the
// person has it.
func (s *screen) addOnPC(c *ui.Context, st *addState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.AddOnPcTitle()).Bold()
		switch {
		case st.located != "":
			components.Text(c, locales.AddOnPcLocated(st.located)).TextColor(t.TextMuted).Wrap()
		case st.found != nil:
			components.Text(c, locales.AddOnPcFound(st.found.Name, st.found.Version)).TextColor(t.TextMuted).Wrap()
		}
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Wrap().Children(func() {
			components.Checkbox(c, &st.alreadyHave, locales.AddAlreadyHave())
			if components.Button(c, locales.AddOnPcLocate(), components.ButtonSecondary).Clicked() {
				s.locateForAdd(st)
			}
		})
	})
}

// locateForAdd asks for the program the person has, and offers its version. The
// dialog blocks until it closes, so it runs off the UI thread.
func (s *screen) locateForAdd(st *addState) {
	s.browseProgram(locales.AddOnPcChoose(), "", func(path string) {
		version, err := s.platform.FileVersion(path)
		if err != nil && !errors.Is(err, platform.ErrNotSupported) {
			log.Printf("read the version of %s: %v", path, err)
		}
		s.onUI(func() {
			if s.add != st {
				return
			}
			st.located, st.alreadyHave = path, true
			if version != "" {
				st.version = version
			}
		})
	})
}

// addLocation is the dialog's "Install location": the folder a Portable app
// goes in, the default one unless the person chose another. note says setup
// programs are only asked to use it.
func (s *screen) addLocation(c *ui.Context, st *addState, note bool) {
	t := c.Theme()
	labelled(c, locales.AddInstallLocation(), func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
			where := st.folder
			if where == "" {
				where = locales.AddInstallLocationDefault()
			}
			components.Text(c, where).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
			if components.Button(c, locales.DownloadFolderBrowse(), components.ButtonSecondary).Clicked() {
				s.browseInstallFolder(st)
			}
			if st.folder != "" {
				if components.Button(c, locales.AddInstallLocationUseDefault(), components.ButtonGhost).Clicked() {
					st.folder, st.folderProblem = "", ""
				}
			}
		})
		if st.folderProblem != "" {
			components.Text(c, st.folderProblem).TextColor(t.Danger).Wrap()
		}
		if note {
			components.Text(c, locales.AddInstallLocationNote()).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
		}
	})
}

// browseInstallFolder asks where to install, and refuses a folder that is too
// broad to manage. The dialog blocks until it closes, so it runs off the UI thread.
func (s *screen) browseInstallFolder(st *addState) {
	s.browseFolder(locales.AddInstallLocationChoose(), st.folder, func(path string) {
		problem := s.installFolderProblem(path)
		s.onUI(func() {
			if s.add != st {
				return
			}
			if problem != "" {
				st.folderProblem = problem
				return
			}
			st.folder, st.folderProblem = path, ""
		})
	})
}

// installFolderProblem says why a folder cannot be an install folder, empty
// when it can.
func (s *screen) installFolderProblem(path string) string {
	folders, err := s.platform.Folders()
	if err != nil {
		log.Printf("locate the user's folders: %v", err)
		return ""
	}
	if install.ValidateFolder(path, folders.Protected) != nil {
		return locales.InstallFolderTooBroad()
	}
	return ""
}

// addDownload is the dialog's "Version and file" part: which release, which of
// its files and what the Item is. It returns what the dialog stands on, and
// whether there is a release to stand on.
func (s *screen) addDownload(c *ui.Context, st *addState, draft catalog.Entry) (downloadView, bool) {
	release, shown := st.shown(draft)
	if !shown {
		return downloadView{}, false
	}
	view := newDownloadView(draft, release, st.file)
	if st.file != "" && view.chosen == "" {
		st.file = "" // The release no longer has the file.
		view = newDownloadView(draft, release, "")
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		versions := draft.Versions()
		options := make([]option, len(versions))
		for i, r := range versions {
			options[i] = option{r.Tag, r.Tag}
			if _, ok := draft.Resolve(r).Pick(); !ok {
				options[i].label = locales.AddVersionNoDownload(r.Tag)
			}
		}
		labelled(c, locales.AddVersion(), func() {
			tag, changed := dropdown(c, locales.ChooseVersion(), release.Tag, options)
			if changed {
				st.tag, st.file = tag, ""
			}
		})
		if name, changed := fileChooser(c, view); changed {
			st.file = name
		}
		downloadNotes(c, view, locales.DownloadNone(), func() { st.kind = selection.ItemApp })
		if kind, changed := kindChooser(c, st.kind); changed {
			st.kind, st.file = kind, ""
		}
	})
	return view, true
}

// adoptedOnPC is the install record of the program the person has: the one they
// pointed at, else the one found in the installed programs. It is nil when
// there is none, and only the version is kept.
func adoptedOnPC(st *addState, repo string) *install.Record {
	switch {
	case st.located != "":
		rec := install.AdoptLocated(st.located)
		return &rec
	case st.found != nil:
		rec := install.AdoptEntry(*st.found, repo)
		return &rec
	}
	return nil
}

// confirmAdd tracks the previewed repository in the background.
func (s *screen) confirmAdd(st *addState, view downloadView, shown bool) {
	preview := *st.preview
	options := st.options()
	if shown && view.chosen != "" {
		chosen := selection.IntentOf(view.pick, true)
		options.Intent = &chosen
	}
	if st.alreadyHave {
		options.Install = adoptedOnPC(st, preview.Repository.Name)
		if options.Install != nil {
			if tag := preview.Draft(options).TagForVersion(st.version); tag != "" {
				options.InstalledVersion = tag
			}
		}
	}
	name := preview.Repository.Ref().String()
	// Installing needs a release with a file to install; the dialog's own
	// choice of release and file is what is installed.
	installTag := ""
	if shown && st.installAfter && !st.alreadyHave && view.hasPick {
		installTag = view.release.Tag
	}
	s.navigate("library")
	s.background(func(ctx context.Context) error {
		e, err := s.catalog.Add(ctx, preview, options)
		switch {
		case errors.Is(err, catalog.ErrExists):
			s.toast(components.ToastInfo, locales.ToastAlreadyTracked(e.Title()))
			return nil
		case ctx.Err() != nil:
			return fmt.Errorf("add %s: %w", name, ctx.Err())
		case err != nil:
			s.record(ctx, activity.Error, 0, locales.ActivityAddFailed(name, err.Error()))
			s.toast(components.ToastError, locales.ActivityAddFailed(name, err.Error()))
			return fmt.Errorf("add %s: %w", name, err)
		}
		s.logSelection(e.Item.ID)
		s.record(ctx, activity.Success, e.Item.ID, locales.ActivityAdded(e.FullName()))
		s.toast(components.ToastSuccess, locales.ToastAdded(e.Title()))
		if installTag != "" {
			s.startInstall(e.Item.ID, installTag, nil)
		}
		return nil
	})
}
