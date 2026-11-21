package background

import "errors"

// ErrNotifications is a notification the system would not show.
var ErrNotifications = errors.New("notifications are not available")

// Notice is a desktop notification. Clicking one is reported to the
// application by ID.
type Notice struct {
	ID    string
	Title string
	Body  string
}

// MenuItem is an entry of the tray icon's menu; an empty Label is a separator.
type MenuItem struct {
	Label string
	Click func()
}

// TrayOptions describes a tray icon.
type TrayOptions struct {
	ToolTip string
	Menu    []MenuItem
	// OnOpen is called when the icon is clicked, where the system lets a click
	// do something other than show the menu.
	OnOpen func()
}

// Tray is an icon in the notification area.
type Tray interface {
	// Set replaces the tooltip and the menu.
	Set(toolTip string, menu []MenuItem)
	// Destroy removes the icon.
	Destroy()
}

// Desktop is what the application asks of the desktop apart from its window:
// the tray icon, notifications, starting at login, and being told when the
// computer wakes or the network changes. MyGo implements it; tests use Fake.
type Desktop interface {
	// NewTray puts the application's icon in the notification area.
	NewTray(TrayOptions) (Tray, error)
	// Notify shows a notification, returning once the system has it. It can
	// wait for the system, so it is not called from a view or a subscriber.
	Notify(Notice) error
	// StartsAtLogin reports whether the system starts the application when the
	// user signs in.
	StartsAtLogin() bool
	SetStartAtLogin(on bool) error
	// OpenedAtLogin reports that the system started this run because the user
	// signed in.
	OpenedAtLogin() bool
	// Online reports whether the computer has a network connection.
	Online() bool
	// OnOnline and OnResume call fn when the connection changes and when the
	// computer wakes from sleep.
	OnOnline(fn func(online bool)) (off func())
	OnResume(fn func()) (off func())
}
