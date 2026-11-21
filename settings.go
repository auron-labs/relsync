package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/auron-labs/mygo-framework/settings"

	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/backup"
	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/selection"
)

var (
	startAtLoginKey = settings.NewKey("general.start_at_login", false, nil)
	// startMinimizedKey keeps the application in the tray when the system started it at sign-in.
	startMinimizedKey = settings.NewKey("general.start_minimized", true, nil)
	showTrayIconKey   = settings.NewKey("general.show_tray_icon", true, nil)
	closeToTrayKey    = settings.NewKey("general.close_to_tray", true, nil)
	// trayNoticeShownKey records that the one-off notice about the application
	// staying in the tray has been shown.
	trayNoticeShownKey = settings.NewKey("general.tray_notice_shown", false, nil)

	themeKey = settings.NewKey("app.theme", themeSystem, oneOf(themeSystem, themeLight, themeDark))
	// errorReportsKey only matters to builds with a Sentry DSN.
	errorReportsKey = settings.NewKey("app.error_reports", true, nil)

	checkOnStartupKey = settings.NewKey("updates.check_on_startup", true, nil)
	parallelChecksKey = settings.NewKey("updates.parallel_checks", defaultParallelChecks, func(n int) error {
		if n < minParallelChecks || n > maxParallelChecks {
			return fmt.Errorf("%d is not between %d and %d", n, minParallelChecks, maxParallelChecks)
		}
		return nil
	})
	checkIntervalKey = settings.NewKey("updates.check_interval", background.DefaultInterval, oneOf(intervalValues()...))
	notifyUpdatesKey = settings.NewKey("updates.notify", true, nil)
	// autoInstallKey installs updates without asking, where that is safe with nobody watching.
	autoInstallKey        = settings.NewKey("updates.auto_install", false, nil)
	defaultPrereleasesKey = settings.NewKey("updates.default_prereleases", false, nil)
	// lastFullCheckKey holds the end of the last full check as Unix seconds, 0 for never.
	lastFullCheckKey = settings.NewKey("updates.last_full_check", int64(0), nil)
	useGitHubCLIKey  = settings.NewKey("github.use_cli_token", false, nil)

	architectureKey = settings.NewKey("install.architecture", archAuto,
		oneOf(archAuto, selection.ArchX64, selection.ArchARM64, selection.ArchX86))
	preferInstallersKey = settings.NewKey("install.prefer_installers", true, nil)
	// downloadFolderKey is where downloads go; empty uses the default in the cache directory.
	downloadFolderKey  = settings.NewKey("install.download_folder", "", absoluteOrEmpty)
	keepDownloadsKey   = settings.NewKey("install.keep_downloads", false, nil)
	verifyDownloadsKey = settings.NewKey("security.verify_downloads", true, nil)
	// The checks beyond a checksum and a signature, and the hints shown before a
	// repository is added. The VirusTotal key is in the credential store.
	scanDownloadsKey     = settings.NewKey("security.scan_downloads", true, nil)
	checkProvenanceKey   = settings.NewKey("security.check_provenance", true, nil)
	useVirusTotalKey     = settings.NewKey("security.use_virustotal", false, nil)
	safetyHintsKey       = settings.NewKey("security.safety_hints", true, nil)
	backupBeforeKey      = settings.NewKey("backup.before_changes", true, nil)
	libraryBackupKey     = settings.NewKey("backup.library_every", background.BackupOff, oneOf(backupIntervalValues()...))
	libraryFolderKey     = settings.NewKey("backup.library_folder", "", absoluteOrEmpty)
	libraryIncludeKey    = settings.NewKey("backup.library_include", string(backup.LibraryOnly), oneOf(string(backup.LibraryOnly), string(backup.WithApps)))
	libraryKeepKey       = settings.NewKey("backup.library_keep", defaultBackupsKept, oneOfInt(backupsKept...))
	lastLibraryBackupKey = settings.NewKey("backup.library_last", int64(0), nil)

	// The sync of the Library across devices: where the shared file is kept
	// (syncOff, syncGist or syncFolder), the gist or folder it is in, whether it
	// is locked with a passphrase (kept in the credential store), and how this
	// device is known. The device id is made on first use.
	syncModeKey       = settings.NewKey("sync.mode", syncOff, oneOf(syncOff, syncGist, syncFolder))
	syncGistKey       = settings.NewKey("sync.gist_id", "", nil)
	syncFolderKey     = settings.NewKey("sync.folder", "", absoluteOrEmpty)
	syncEncryptKey    = settings.NewKey("sync.encrypt", false, nil)
	syncDeviceIDKey   = settings.NewKey("sync.device_id", "", nil)
	syncDeviceNameKey = settings.NewKey("sync.device_name", "", nil)

	installAfterAddingKey = settings.NewKey("install.install_after_adding", true, nil)
	// portableFolderKey and toolsFolderKey are where portable apps and tools are
	// installed; empty uses the default folders.
	portableFolderKey     = settings.NewKey("install.portable_folder", "", absoluteOrEmpty)
	toolsFolderKey        = settings.NewKey("install.tools_folder", "", absoluteOrEmpty)
	startMenuShortcutsKey = settings.NewKey("install.start_menu_shortcuts", true, nil)
	confirmRemoveKey      = settings.NewKey("general.confirm_remove", true, nil)
	// silentInstallsKey runs setup programs and uninstallers without their
	// interface where they can; installForEveryoneKey installs apps for every
	// user of the PC.
	silentInstallsKey     = settings.NewKey("install.silent_installs", true, nil)
	installForEveryoneKey = settings.NewKey("install.for_everyone", false, nil)

	// The Discover page's choices. Light mode and the download filter spend
	// fewer or more of the GitHub request allowance; the rest is where the
	// person left the page.
	discoverLightKey         = settings.NewKey("discover.light", false, nil)
	discoverDownloadsOnlyKey = settings.NewKey("discover.downloads_only", false, nil)
	discoverIntroSeenKey     = settings.NewKey("discover.intro_seen", false, nil)
	discoverTabKey           = settings.NewKey("discover.tab", string(discover.Trending), func(tab string) error {
		if !discover.Tab(tab).Valid() {
			return fmt.Errorf("%q is not a Discover list", tab)
		}
		return nil
	})
	discoverCategoryKey = settings.NewKey("discover.category", discover.AllKey, func(key string) error {
		if discover.Find(key).Key != key {
			return fmt.Errorf("%q is not a Discover category", key)
		}
		return nil
	})
	discoverCompareKey = settings.NewKey("discover.compare", string(discover.Day), oneOf(string(discover.Day), string(discover.Week)))

	// The Library's layout: grid or list, whether Items without a group of their
	// own are shown in their category, and whether the Library is split into
	// sections by group.
	libraryViewKey   = settings.NewKey("library.view", viewGrid, oneOf(viewGrid, viewList))
	autoGroupsKey    = settings.NewKey("library.auto_groups", true, nil)
	groupSectionsKey = settings.NewKey("library.group_sections", true, nil)

	// scanFoldersKey holds the folders the user asked the PC scan to look in.
	scanFoldersKey = settings.NewKey("scan.folders", []string(nil), func(folders []string) error {
		for _, f := range folders {
			if err := absoluteOrEmpty(f); err != nil {
				return err
			}
		}
		return nil
	})
)

// backupsKept are the choices of how many Library backups to keep; 0 keeps all.
var backupsKept = []int{5, 10, 30, 100, 0}

const defaultBackupsKept = 10

func backupIntervalValues() []string {
	values := make([]string, len(background.BackupIntervals))
	for i, o := range background.BackupIntervals {
		values[i] = o.Value
	}
	return values
}

func oneOfInt(allowed ...int) func(int) error {
	return func(value int) error {
		if !slices.Contains(allowed, value) {
			return fmt.Errorf("%d is not one of %v", value, allowed)
		}
		return nil
	}
}

func intervalValues() []string {
	values := make([]string, len(background.Intervals))
	for i, o := range background.Intervals {
		values[i] = o.Value
	}
	return values
}

func absoluteOrEmpty(folder string) error {
	if folder != "" && !filepath.IsAbs(folder) {
		return fmt.Errorf("%q is not an absolute path", folder)
	}
	return nil
}

const (
	defaultParallelChecks = 4
	minParallelChecks     = 1
	maxParallelChecks     = 8
)

// archAuto asks for the architecture of this PC.
const archAuto = "auto"

// Where the Library is synced to.
const (
	syncOff    = "off"
	syncGist   = "gist"
	syncFolder = "folder"
)

// The Library's layouts.
const (
	viewGrid = "grid"
	viewList = "list"
)

const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

// preferences is the typed set of values persisted by the application.
type preferences struct {
	Theme        string
	ErrorReports bool

	// StartAtLogin starts the application when the user signs in.
	StartAtLogin bool
	// StartMinimized keeps a start made at sign-in in the tray.
	StartMinimized bool
	ShowTrayIcon   bool
	// CloseToTray hides the window instead of quitting when it is closed, while
	// the tray icon is showing.
	CloseToTray bool
	// TrayNoticeShown is set once the notice that the application keeps running
	// in the tray has been shown.
	TrayNoticeShown bool

	// CheckInterval is how often updates are checked for, one of background.Intervals.
	CheckInterval string
	// NotifyUpdates shows a notification when updates are found or installed.
	NotifyUpdates bool
	// AutoInstall installs updates without asking, where that is safe with nobody watching.
	AutoInstall bool

	CheckOnStartup            bool
	ParallelChecks            int
	DefaultIncludePrereleases bool
	// LastFullCheck is when the last full check ended, in Unix seconds; 0 for never.
	LastFullCheck int64
	UseGitHubCLI  bool

	// PreferredArchitecture is archAuto, or the architecture whose builds to
	// download.
	PreferredArchitecture string
	// PreferInstallers chooses a setup program over a portable archive.
	PreferInstallers bool

	// DownloadFolder is where downloaded files go; empty is the default folder.
	DownloadFolder string
	// KeepDownloads keeps finished downloads; otherwise the next start removes them.
	KeepDownloads bool
	// VerifyDownloads checks the checksum and signature of every download.
	VerifyDownloads bool
	// ScanDownloads has Windows Security scan every download. CheckProvenance
	// asks GitHub for a signed build record of it, and UseVirusTotal looks its
	// hash up on VirusTotal with the person's own key.
	ScanDownloads   bool
	CheckProvenance bool
	UseVirusTotal   bool
	// SafetyHints shows plain-language signals about a repository when adding it.
	SafetyHints bool

	// BackupBeforeChanges zips a Portable app's folder before it is updated or removed.
	BackupBeforeChanges bool
	// LibraryBackupEvery is how often the Library is backed up, one of
	// background.BackupIntervals; LibraryBackupFolder is where, empty for the
	// default folder; LibraryBackupInclude is a backup.Include and
	// LibraryBackupKeep how many backups to keep, 0 for all.
	LibraryBackupEvery   string
	LibraryBackupFolder  string
	LibraryBackupInclude string
	LibraryBackupKeep    int
	// LastLibraryBackup is when the last one finished, in Unix seconds; 0 for never.
	LastLibraryBackup int64

	// SyncMode is syncOff, syncGist or syncFolder. SyncGistID is the gist the
	// library is kept in, empty until it is found or made, and SyncFolder the
	// folder that is shared. SyncEncrypt says the shared file is locked with
	// the passphrase in the credential store. SyncDeviceID names this device
	// to the others, and SyncDeviceName is what the user calls it, empty for
	// the computer's name.
	SyncMode       string
	SyncGistID     string
	SyncFolder     string
	SyncEncrypt    bool
	SyncDeviceID   string
	SyncDeviceName string

	// InstallAfterAdding starts installing a repository as soon as it is added.
	InstallAfterAdding bool
	// PortableFolder and ToolsFolder are where portable apps and tools are
	// installed; empty is the default folder.
	PortableFolder string
	ToolsFolder    string
	// StartMenuShortcuts makes a Start menu shortcut for each portable app.
	StartMenuShortcuts bool
	// ConfirmRemove asks before an app is uninstalled or stopped being tracked.
	ConfirmRemove bool
	// SilentInstalls runs setup programs and uninstallers without their
	// interface where they have a way to.
	SilentInstalls bool
	// InstallForEveryone installs apps with a setup program for every user of
	// the PC, and prefers downloads built that way.
	InstallForEveryone bool

	// ScanFolders are folders of portable programs the PC scan looks in.
	ScanFolders []string

	// LibraryView is viewGrid or viewList.
	LibraryView string
	// AutoGroups shows an Item that has no group of its own in its category.
	AutoGroups bool
	// GroupSections splits the Library into collapsible sections by group.
	GroupSections bool

	// DiscoverLight shows fewer projects per list and prepares nothing ahead.
	DiscoverLight bool
	// DiscoverDownloadsOnly hides projects with no download for this PC.
	DiscoverDownloadsOnly bool
	// DiscoverIntroSeen is set once the explainer of the request allowance has been dismissed.
	DiscoverIntroSeen bool
	// DiscoverTab, DiscoverCategory and DiscoverCompare are where the Discover
	// page was left: a discover.Tab, a category key and a discover.Window.
	DiscoverTab      string
	DiscoverCategory string
	DiscoverCompare  string
}

// defaultPreferences are the values of a fresh installation.
func defaultPreferences() preferences {
	return preferences{
		Theme: themeSystem, ErrorReports: true, CheckOnStartup: true, ParallelChecks: defaultParallelChecks,
		StartMinimized: true, ShowTrayIcon: true, CloseToTray: true,
		CheckInterval: background.DefaultInterval, NotifyUpdates: true,
		PreferredArchitecture: archAuto, PreferInstallers: true, VerifyDownloads: true,
		ScanDownloads: true, CheckProvenance: true, SafetyHints: true, BackupBeforeChanges: true,
		LibraryBackupEvery: background.BackupOff, LibraryBackupInclude: string(backup.LibraryOnly), LibraryBackupKeep: defaultBackupsKept,
		InstallAfterAdding: true, StartMenuShortcuts: true, ConfirmRemove: true, SilentInstalls: true,
		DiscoverTab: string(discover.Trending), DiscoverCategory: discover.AllKey, DiscoverCompare: string(discover.Day),
		LibraryView: viewGrid, AutoGroups: true, GroupSections: true, SyncMode: syncOff,
	}
}

// lastFullCheck is when the last full check ended, and whether one has.
// checkEvery is the time between scheduled checks, zero for manual only.
func (p preferences) checkEvery() time.Duration { return background.IntervalEvery(p.CheckInterval) }

// backupEvery is the time between Library backups, zero for none.
func (p preferences) backupEvery() time.Duration { return background.BackupEvery(p.LibraryBackupEvery) }

// lastLibraryBackup is when the last Library backup ended, and whether one has.
func (p preferences) lastLibraryBackup() (time.Time, bool) {
	if p.LastLibraryBackup == 0 {
		return time.Time{}, false
	}
	return time.Unix(p.LastLibraryBackup, 0), true
}

func (p preferences) lastFullCheck() (time.Time, bool) {
	if p.LastFullCheck == 0 {
		return time.Time{}, false
	}
	return time.Unix(p.LastFullCheck, 0), true
}

func loadPreferences(ctx context.Context, values *settings.Values) (preferences, error) {
	var p preferences
	var errs [53]error
	p.Theme, errs[0] = settings.Get(ctx, values, themeKey)
	p.ErrorReports, errs[1] = settings.Get(ctx, values, errorReportsKey)
	p.CheckOnStartup, errs[2] = settings.Get(ctx, values, checkOnStartupKey)
	p.ParallelChecks, errs[3] = settings.Get(ctx, values, parallelChecksKey)
	p.DefaultIncludePrereleases, errs[4] = settings.Get(ctx, values, defaultPrereleasesKey)
	p.LastFullCheck, errs[5] = settings.Get(ctx, values, lastFullCheckKey)
	p.UseGitHubCLI, errs[6] = settings.Get(ctx, values, useGitHubCLIKey)
	p.PreferredArchitecture, errs[7] = settings.Get(ctx, values, architectureKey)
	p.PreferInstallers, errs[8] = settings.Get(ctx, values, preferInstallersKey)
	p.DownloadFolder, errs[9] = settings.Get(ctx, values, downloadFolderKey)
	p.KeepDownloads, errs[10] = settings.Get(ctx, values, keepDownloadsKey)
	p.VerifyDownloads, errs[11] = settings.Get(ctx, values, verifyDownloadsKey)
	p.InstallAfterAdding, errs[12] = settings.Get(ctx, values, installAfterAddingKey)
	p.PortableFolder, errs[13] = settings.Get(ctx, values, portableFolderKey)
	p.ToolsFolder, errs[14] = settings.Get(ctx, values, toolsFolderKey)
	p.StartMenuShortcuts, errs[15] = settings.Get(ctx, values, startMenuShortcutsKey)
	p.ConfirmRemove, errs[16] = settings.Get(ctx, values, confirmRemoveKey)
	p.SilentInstalls, errs[17] = settings.Get(ctx, values, silentInstallsKey)
	p.InstallForEveryone, errs[18] = settings.Get(ctx, values, installForEveryoneKey)
	p.StartAtLogin, errs[19] = settings.Get(ctx, values, startAtLoginKey)
	p.StartMinimized, errs[20] = settings.Get(ctx, values, startMinimizedKey)
	p.ShowTrayIcon, errs[21] = settings.Get(ctx, values, showTrayIconKey)
	p.CloseToTray, errs[22] = settings.Get(ctx, values, closeToTrayKey)
	p.TrayNoticeShown, errs[23] = settings.Get(ctx, values, trayNoticeShownKey)
	p.CheckInterval, errs[24] = settings.Get(ctx, values, checkIntervalKey)
	p.NotifyUpdates, errs[25] = settings.Get(ctx, values, notifyUpdatesKey)
	p.AutoInstall, errs[26] = settings.Get(ctx, values, autoInstallKey)
	p.ScanFolders, errs[27] = settings.Get(ctx, values, scanFoldersKey)
	p.DiscoverLight, errs[28] = settings.Get(ctx, values, discoverLightKey)
	p.DiscoverDownloadsOnly, errs[29] = settings.Get(ctx, values, discoverDownloadsOnlyKey)
	p.DiscoverIntroSeen, errs[30] = settings.Get(ctx, values, discoverIntroSeenKey)
	p.DiscoverTab, errs[31] = settings.Get(ctx, values, discoverTabKey)
	p.DiscoverCategory, errs[32] = settings.Get(ctx, values, discoverCategoryKey)
	p.DiscoverCompare, errs[33] = settings.Get(ctx, values, discoverCompareKey)
	p.LibraryView, errs[34] = settings.Get(ctx, values, libraryViewKey)
	p.AutoGroups, errs[35] = settings.Get(ctx, values, autoGroupsKey)
	p.GroupSections, errs[36] = settings.Get(ctx, values, groupSectionsKey)
	p.ScanDownloads, errs[37] = settings.Get(ctx, values, scanDownloadsKey)
	p.CheckProvenance, errs[38] = settings.Get(ctx, values, checkProvenanceKey)
	p.UseVirusTotal, errs[39] = settings.Get(ctx, values, useVirusTotalKey)
	p.SafetyHints, errs[40] = settings.Get(ctx, values, safetyHintsKey)
	p.BackupBeforeChanges, errs[41] = settings.Get(ctx, values, backupBeforeKey)
	p.LibraryBackupEvery, errs[42] = settings.Get(ctx, values, libraryBackupKey)
	p.LibraryBackupFolder, errs[43] = settings.Get(ctx, values, libraryFolderKey)
	p.LibraryBackupInclude, errs[44] = settings.Get(ctx, values, libraryIncludeKey)
	p.LibraryBackupKeep, errs[45] = settings.Get(ctx, values, libraryKeepKey)
	p.LastLibraryBackup, errs[46] = settings.Get(ctx, values, lastLibraryBackupKey)
	p.SyncMode, errs[47] = settings.Get(ctx, values, syncModeKey)
	p.SyncGistID, errs[48] = settings.Get(ctx, values, syncGistKey)
	p.SyncFolder, errs[49] = settings.Get(ctx, values, syncFolderKey)
	p.SyncEncrypt, errs[50] = settings.Get(ctx, values, syncEncryptKey)
	p.SyncDeviceID, errs[51] = settings.Get(ctx, values, syncDeviceIDKey)
	p.SyncDeviceName, errs[52] = settings.Get(ctx, values, syncDeviceNameKey)
	return p, errors.Join(errs[:]...)
}

func (p preferences) save(ctx context.Context, values *settings.Values) error {
	return errors.Join(
		settings.Set(ctx, values, themeKey, p.Theme),
		settings.Set(ctx, values, errorReportsKey, p.ErrorReports),
		settings.Set(ctx, values, checkOnStartupKey, p.CheckOnStartup),
		settings.Set(ctx, values, parallelChecksKey, p.ParallelChecks),
		settings.Set(ctx, values, defaultPrereleasesKey, p.DefaultIncludePrereleases),
		settings.Set(ctx, values, lastFullCheckKey, p.LastFullCheck),
		settings.Set(ctx, values, useGitHubCLIKey, p.UseGitHubCLI),
		settings.Set(ctx, values, architectureKey, p.PreferredArchitecture),
		settings.Set(ctx, values, preferInstallersKey, p.PreferInstallers),
		settings.Set(ctx, values, downloadFolderKey, p.DownloadFolder),
		settings.Set(ctx, values, keepDownloadsKey, p.KeepDownloads),
		settings.Set(ctx, values, verifyDownloadsKey, p.VerifyDownloads),
		settings.Set(ctx, values, installAfterAddingKey, p.InstallAfterAdding),
		settings.Set(ctx, values, portableFolderKey, p.PortableFolder),
		settings.Set(ctx, values, toolsFolderKey, p.ToolsFolder),
		settings.Set(ctx, values, startMenuShortcutsKey, p.StartMenuShortcuts),
		settings.Set(ctx, values, confirmRemoveKey, p.ConfirmRemove),
		settings.Set(ctx, values, silentInstallsKey, p.SilentInstalls),
		settings.Set(ctx, values, installForEveryoneKey, p.InstallForEveryone),
		settings.Set(ctx, values, startAtLoginKey, p.StartAtLogin),
		settings.Set(ctx, values, startMinimizedKey, p.StartMinimized),
		settings.Set(ctx, values, showTrayIconKey, p.ShowTrayIcon),
		settings.Set(ctx, values, closeToTrayKey, p.CloseToTray),
		settings.Set(ctx, values, trayNoticeShownKey, p.TrayNoticeShown),
		settings.Set(ctx, values, checkIntervalKey, p.CheckInterval),
		settings.Set(ctx, values, notifyUpdatesKey, p.NotifyUpdates),
		settings.Set(ctx, values, autoInstallKey, p.AutoInstall),
		settings.Set(ctx, values, scanFoldersKey, p.ScanFolders),
		settings.Set(ctx, values, discoverLightKey, p.DiscoverLight),
		settings.Set(ctx, values, discoverDownloadsOnlyKey, p.DiscoverDownloadsOnly),
		settings.Set(ctx, values, discoverIntroSeenKey, p.DiscoverIntroSeen),
		settings.Set(ctx, values, discoverTabKey, p.DiscoverTab),
		settings.Set(ctx, values, discoverCategoryKey, p.DiscoverCategory),
		settings.Set(ctx, values, discoverCompareKey, p.DiscoverCompare),
		settings.Set(ctx, values, libraryViewKey, p.LibraryView),
		settings.Set(ctx, values, autoGroupsKey, p.AutoGroups),
		settings.Set(ctx, values, groupSectionsKey, p.GroupSections),
		settings.Set(ctx, values, scanDownloadsKey, p.ScanDownloads),
		settings.Set(ctx, values, checkProvenanceKey, p.CheckProvenance),
		settings.Set(ctx, values, useVirusTotalKey, p.UseVirusTotal),
		settings.Set(ctx, values, safetyHintsKey, p.SafetyHints),
		settings.Set(ctx, values, backupBeforeKey, p.BackupBeforeChanges),
		settings.Set(ctx, values, libraryBackupKey, p.LibraryBackupEvery),
		settings.Set(ctx, values, libraryFolderKey, p.LibraryBackupFolder),
		settings.Set(ctx, values, libraryIncludeKey, p.LibraryBackupInclude),
		settings.Set(ctx, values, libraryKeepKey, p.LibraryBackupKeep),
		settings.Set(ctx, values, lastLibraryBackupKey, p.LastLibraryBackup),
		settings.Set(ctx, values, syncModeKey, p.SyncMode),
		settings.Set(ctx, values, syncGistKey, p.SyncGistID),
		settings.Set(ctx, values, syncFolderKey, p.SyncFolder),
		settings.Set(ctx, values, syncEncryptKey, p.SyncEncrypt),
		settings.Set(ctx, values, syncDeviceIDKey, p.SyncDeviceID),
		settings.Set(ctx, values, syncDeviceNameKey, p.SyncDeviceName),
	)
}

func oneOf(allowed ...string) func(string) error {
	return func(value string) error {
		if !slices.Contains(allowed, value) {
			return fmt.Errorf("%q is not one of %v", value, allowed)
		}
		return nil
	}
}
