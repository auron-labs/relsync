package main

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/backup"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/scan"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
	"github.com/oneo-me/onevra/source/shells/sidebar"
	"github.com/oneo-me/onevra/source/theme"
)

const (
	windowWidth, windowHeight       = 1280, 820
	windowMinWidth, windowMinHeight = 680, 520
	// Below compactBreakpoint the sidebar narrows to an icon rail.
	compactBreakpoint = 980
	navExpandedWidth  = 252
	navCompactWidth   = 72
	drawerWidth       = 440
	drawerMinWidth    = 320
	drawerMaxWidth    = 560
)

// page is one destination in the sidebar. Add a page by appending to pages.
type page struct {
	id    string
	title func() string
	icon  func() *ui.SVG
	// badge, when set, is the count shown on the navigation item; 0 shows none.
	badge func() int
	view  func(*ui.Context)
	// footer places the item with Settings at the bottom of the sidebar.
	footer bool
}

// screen is the retained application state the views read.
type screen struct {
	services
	prefs *state.Cell[preferences]
	saver *state.Saver[preferences]
	jobs  *jobs.Manager
	dirs  dirs
	// win and shell exist once the window opens; views and tests work without them.
	win   *mygo.Window
	shell *sidebar.Shell
	// page is the selected navigation id.
	page string
	// compact reports whether the window is below the sidebar breakpoint.
	compact bool
	// expandedNavWidth is the sidebar width to restore above the breakpoint,
	// so crossing it does not lose a width the user chose by dragging.
	expandedNavWidth float32
	omnibox          string
	settings         components.SettingsState
	// counts feed the navigation badges; later phases write them.
	updateCount   *state.Cell[int]
	tryLaterCount *state.Cell[int]
	// update is the newer version found at launch, if the build checks for updates.
	update *mygo.Update
	drawer drawer
	// dialog is the modal dialog showing, if any.
	dialog     *appDialog
	dialogOpen bool
	// toasts holds messages queued for the next frame; AddToast is a view API.
	toasts []ui.Toast
	// reveal opens a folder in the system file manager; replaceable in tests.
	reveal func(path string) error
	// showFile reveals a file in the system file manager; replaceable in tests.
	showFile func(path string)
	// openURL opens a web address in the browser; replaceable in tests.
	openURL func(address string) error
	// copyText puts text on the clipboard, and savePath asks where to save a
	// file; replaceable in tests.
	copyText func(text string)
	savePath func(opts mygo.SaveDialogOptions) (string, error)
	// ghToken reads the GitHub CLI's token; replaceable in tests.
	ghToken func(ctx context.Context) (string, error)

	// desktop is the tray icon, notifications and start at login; replaceable in tests.
	desktop background.Desktop
	// sched runs the update checks on their interval; selfSched looks for a
	// newer version of the application.
	sched, selfSched *background.Scheduler
	// backupSched runs the Library backups on their interval; backupNotified
	// says the person has been told the backup folder cannot be reached, so
	// the next attempt does not tell them again.
	backupSched    *background.Scheduler
	backupNotified atomic.Bool
	// devices keeps the Library in step with the user's other devices.
	devices *deviceSync
	// syncPage is what the sharing settings remember between frames.
	syncPage syncSettingsUI
	// relaunch quits the application and starts it again; replaceable in tests.
	relaunch func()
	presence presence
	// hide hides the window while the application keeps running; replaceable in tests.
	hide func()

	// transfers are the downloads running and those whose outcome the person has not dismissed.
	transfers *state.Cell[[]transfer]
	// operations are the installs, updates and removals under way, one per Item.
	operations *state.Cell[[]operation]
	// installTiming is how long installs wait for the system's list of installed
	// programs to change after a setup program or uninstaller ends; tests shorten it.
	installTiming install.Timing
	// asking admits one question at a time.
	asking chan struct{}
	// filesOpen holds the releases whose list of every file is expanded, by
	// Item and tag.
	filesOpen map[string]bool
	// backups are the Portable apps' backups read from disk, by Item. They are
	// loaded in the background, so the views never read the disk.
	backups map[int64][]backup.AppInfo

	library libraryState
	// discoverUI is the Discover page's lists and choices; tryLaterUI is the
	// Try later page's search, sort and view.
	discoverUI discoverState
	tryLaterUI tryLaterState
	// scanPage is the Scan page's results and what it remembers between frames.
	scanPage scanUI
	// scanLocations are where the PC scan finds other managers' records.
	scanLocations scan.Locations
	// searchPace is the time between searches by name; zero follows the search
	// allowance, which a token raises. Tests shorten it.
	searchPace time.Duration
	pacerMu    sync.Mutex
	pacer      *scan.Pacer
	pacerGap   time.Duration
	// add is the add dialog's state while one is open.
	add          *addState
	activityList components.ListState
	markdown     markdown.Options
	// previews memoizes the plain-text note previews the Updates page shows.
	previews map[previewKey]string
	// account is who GitHub requests are made as.
	account *state.Cell[account]
	// argsEdit is the installer arguments, and notesEdit the note and tags,
	// being edited in the drawer.
	argsEdit  argsEditor
	notesEdit notesEditor
	// tokenInput is the token being typed in Settings; tokenNote is the outcome of the last attempt.
	tokenInput string
	tokenNote  string
	// vtKey is the VirusTotal key as Settings shows it.
	vtKey virusTotalState
	// importPage is the Import & Export page's choices and the outcome of the last import.
	importPage importUI
	// actions numbers background actions so each runs as a job of its own.
	actions atomic.Int64
	// queued holds what onUI could not hand to a window.
	queueMu sync.Mutex
	queued  []func()
}

func newScreen(prefs *state.Cell[preferences], saver *state.Saver[preferences], manager *jobs.Manager, d dirs, svc services) *screen {
	s := &screen{
		services: svc,
		prefs:    prefs, saver: saver, jobs: manager, dirs: d,
		expandedNavWidth: navExpandedWidth,
		updateCount:      state.NewCell(countUpdates(svc.catalog.Entries().Get())), tryLaterCount: state.NewCell(len(svc.catalog.TryLaterEntries().Get())),
		reveal: mygo.Shell.OpenPath, showFile: mygo.Shell.ShowItemInFolder, openURL: mygo.Shell.OpenExternal, ghToken: githubCLIToken,
		copyText: mygo.Clipboard.WriteText, savePath: mygo.Dialog.Save,
		account:   state.NewCell(account{}),
		transfers: state.NewCell([]transfer(nil)), filesOpen: map[string]bool{},
		operations: state.NewCell([]operation(nil)), asking: make(chan struct{}, 1),
		desktop: background.MyGo{}, presence: presence{poke: make(chan struct{}, 1)},
		scanPage: newScanUI(), scanLocations: scan.DefaultLocations(), importPage: newImportUI(),
		relaunch: mygo.App.Relaunch,
	}
	s.hide = func() {
		if s.win != nil {
			s.win.Hide()
		}
	}
	clock := background.SystemClock()
	s.sched = background.NewScheduler(clock, func() (time.Time, bool) { return prefs.Get().lastFullCheck() }, func() { s.checkLibrary(false) })
	s.selfSched = background.NewScheduler(clock, func() (time.Time, bool) { return time.Time{}, false }, s.checkForUpdates)
	s.backupSched = background.NewScheduler(clock, func() (time.Time, bool) { return prefs.Get().lastLibraryBackup() }, s.scheduledBackup)
	s.devices = newDeviceSync(clock)
	s.devices.sched = background.NewScheduler(clock, s.devices.lastSynced, func() { s.requestSync(false) })
	svc.catalog.Entries().Subscribe(func(entries []catalog.Entry) { s.updateCount.Set(countUpdates(entries)) })
	svc.catalog.TryLaterEntries().Subscribe(func(saved []catalog.TryLater) { s.tryLaterCount.Set(len(saved)) })
	s.learnSites()
	svc.catalog.SetPolicy(machinePolicy(prefs.Get()))
	prefs.Subscribe(s.policyChanged)
	return s
}

// pages lists the sidebar's destinations; the first is shown at launch.
func (s *screen) pages() []page {
	return []page{
		{id: "library", title: locales.PageLibrary, icon: lucide.Library, view: s.libraryView},
		{id: "updates", title: locales.PageUpdates, icon: lucide.ArrowDownToLine, badge: s.updateCount.Get, view: s.updatesView},
		{id: "discover", title: locales.PageDiscover, icon: lucide.Compass, view: s.discoverView},
		{id: "trylater", title: locales.PageTryLater, icon: lucide.Bookmark, badge: s.tryLaterCount.Get, view: s.tryLaterView},
		{id: "scan", title: locales.PageScan, icon: lucide.ScanSearch, view: s.scanView},
		{id: "import", title: locales.PageImportExport, icon: lucide.ArrowLeftRight, view: s.importView},
		{id: "activity", title: locales.PageActivity, icon: lucide.Activity, view: s.activityView},
		{id: "settings", title: locales.PageSettings, icon: lucide.Settings, view: s.settingsView, footer: true},
	}
}

// currentPage returns the selected page, or the first one when none is.
func (s *screen) currentPage() page {
	pages := s.pages()
	for _, p := range pages {
		if p.id == s.page {
			return p
		}
	}
	return pages[0]
}

func (s *screen) navigate(id string) {
	s.page = id
	if id == "discover" {
		s.discoverEntered()
	}
	if s.win != nil {
		s.win.Invalidate()
	}
}

// openWindow creates the main window on Onevra's sidebar shell. It stays
// hidden until its first frame is drawn, so it opens with its content ready.
func (s *screen) openWindow() *mygo.Window {
	shell := sidebar.NewShell(sidebar.ShellOptions{
		Title: appName, Width: windowWidth, Height: windowHeight,
		MinWidth: windowMinWidth, MinHeight: windowMinHeight,
		StateKey: "main", Theme: theme.Onevra, Hidden: true,
		Left: s.navigation, Content: s.content, ContentTitle: s.contentTitle,
		LeftWidth: navExpandedWidth, LeftMinWidth: navCompactWidth,
		Right: s.drawerView, RightHeader: s.drawerTitle,
		RightWidth: drawerWidth, RightMinWidth: drawerMinWidth, RightMaxWidth: drawerMaxWidth,
		RightCollapsed: true,
	})
	s.shell = shell
	return shell.Window()
}

// navigation builds the sidebar: brand, primary pages, then the footer with
// the update button, the account card, Settings and the version. Below the
// compact breakpoint it collapses to an icon rail.
func (s *screen) navigation(c *ui.Context) {
	width, _ := c.Size()
	compact := width < compactBreakpoint
	if compact != s.compact {
		s.compact = compact
		if s.shell != nil {
			if compact {
				s.expandedNavWidth = s.shell.LeftWidth()
				s.shell.SetLeftWidth(navCompactWidth)
			} else {
				s.shell.SetLeftWidth(s.expandedNavWidth)
			}
		}
	}
	ui.Column(c).Fill().Padding(sidebar.HeaderHeight(c), 0, 0, 0).Children(func() {
		if !s.compact {
			ui.Column(c).FillWidth().Padding(0, 16, 12, 16).Children(func() {
				components.Text(c, appName).FontSize(17).Bold()
			})
		}
		items := func(footer bool) {
			for _, p := range s.pages() {
				if p.footer == footer {
					s.navItem(c, p)
				}
			}
		}
		ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			components.Sidebar(c).Children(func() { items(false) })
		})
		s.footer(c)
	})
}

// navItem builds one navigation entry: a labelled row expanded, an icon with
// a tooltip compact.
func (s *screen) navItem(c *ui.Context, p page) {
	selected := p.id == s.currentPage().id
	if s.compact {
		t := c.Theme()
		b := ui.ButtonBase(c).Key(p.id).Label(p.title()).FillWidth().Justify(ui.Center).
			Padding(t.Space(1.75), 0).Radius(t.Radius).Selected(selected).
			TextColor(t.Text).Background(ui.Color{})
		if selected {
			b.Background(t.Text.Alpha(.1))
		}
		b.Children(func() {
			components.Icon(c, p.icon()).Size(t.Space(4.5), t.Space(4.5)).TextColor(t.Accent)
		})
		components.Tooltip(c, b, p.title())
		if b.Clicked() {
			s.navigate(p.id)
		}
		return
	}
	leading := func(c *ui.Context, size float32) {
		components.Icon(c, p.icon()).Size(size, size).TextColor(c.Theme().Accent)
	}
	var trailing func(c *ui.Context, size float32)
	if p.badge != nil && p.badge() > 0 {
		count := p.badge()
		trailing = func(c *ui.Context, _ float32) {
			components.Badge(c, strconv.Itoa(count), components.BadgeOptions{Tone: components.BadgeAccent})
		}
	}
	if components.SidebarItemWithSlots(c, p.title(), selected, leading, trailing).Key(p.id).Clicked() {
		s.navigate(p.id)
	}
}

// footer builds the sidebar's bottom block.
func (s *screen) footer(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).FillWidth().Shrink(0).Padding(0, t.Space(3), t.Space(2), t.Space(3)).Gap(t.Space(2)).Children(func() {
		s.updateButton(c)
		if !s.compact {
			s.allowanceCard(c)
		}
		for _, p := range s.pages() {
			if p.footer {
				s.navItem(c, p)
			}
		}
		if !s.compact {
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				components.Text(c, locales.VersionLabel(mygo.App.Version())).FontSize(t.Rem(.8)).TextColor(t.TextMuted)
			})
		}
	})
}

// topBar builds the search-or-paste box and the check-for-updates button. The
// box's button adds the repository when the text is one.
func (s *screen) topBar(c *ui.Context) {
	t := c.Theme()
	_, isRepository := s.sources.ParseLink(s.omnibox)
	action := locales.OmniboxSearch()
	if isRepository {
		action = locales.OmniboxAdd()
	}
	checkLabel, checking := locales.CheckForUpdates(), false
	if status, ok := s.jobs.Status(checkAllJob); ok && status.State == jobs.Running {
		checking = true
		if status.Total > 0 {
			checkLabel = locales.CheckProgress(status.Done, status.Total)
		}
	}
	ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(t.Space(3)).
		Padding(0, 24, t.Space(3), 24).Children(func() {
		box := components.TextBox(c, &s.omnibox, components.TextBoxOptions{LeadingIcon: lucide.Search()}).
			Label(locales.OmniboxLabel()).Placeholder(locales.OmniboxPlaceholder()).
			Grow(1).Shrink(1).MinWidth(0).MaxWidth(760)
		submitted := box.Submitted()
		if components.Button(c, action, components.ButtonPrimary).Clicked() || submitted {
			s.submitOmnibox()
		}
		if components.Button(c, checkLabel, components.ButtonSecondary).Disabled(checking).Clicked() {
			s.checkLibrary(true)
		}
	})
}

func (s *screen) contentTitle(c *ui.Context) {
	components.Text(c, s.currentPage().title()).Bold().SingleLine()
}

// content builds the top bar and the selected page below the shell's floating
// header, then the overlay hosts.
func (s *screen) content(c *ui.Context) {
	ui.Column(c).Fill().Padding(sidebar.HeaderHeight(c), 0, 0, 0).Children(func() {
		s.topBar(c)
		if s.saver.Err().Get() != nil {
			t := c.Theme()
			ui.Row(c).FillWidth().Shrink(0).Padding(0, 24, t.Space(2), 24).Children(func() {
				components.Text(c, locales.PreferencesSaveFailed()).TextColor(t.Danger).Wrap()
			})
		}
		ui.Column(c).Fill().MinHeight(0).Children(func() {
			s.currentPage().view(c)
		})
	})
	s.dialogHost(c)
	s.drainToasts(c)
	// Overlays built later hear Escape first; what is left closes the drawer.
	c.OnWindowShortcut(0, ui.KeyEscape, s.closeTopOverlay)
	c.OnWindowShortcut(0, ui.KeyF5, func() { s.checkLibrary(true) })
}

// view lays out the navigation, content and drawer side by side, as the shell
// does, for tests and the MCP harness, which have no window.
func (s *screen) view(c *ui.Context) {
	s.runQueuedUI()
	width, _ := c.Size()
	navWidth := float32(navExpandedWidth)
	if width < compactBreakpoint {
		navWidth = navCompactWidth
	}
	ui.Row(c).Fill().Children(func() {
		ui.Box(c).Width(navWidth).FillHeight().Shrink(0).Children(func() { s.navigation(c) })
		ui.Box(c).Grow(1).FillHeight().MinWidth(0).Children(func() { s.content(c) })
		if s.drawer.open {
			ui.Box(c).Width(drawerWidth).FillHeight().Shrink(0).Children(func() { s.drawerView(c) })
		}
	})
}
