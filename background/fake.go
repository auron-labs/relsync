package background

import (
	"slices"
	"sync"
)

// Fake is a Desktop for tests: it shows nothing, and keeps what it was asked
// to show.
type Fake struct {
	// TrayErr makes NewTray fail; NotifyErr makes Notify fail.
	TrayErr, NotifyErr error
	// OpenedByLogin is what OpenedAtLogin reports.
	OpenedByLogin bool
	// Offline makes Online report false.
	Offline bool

	mu       sync.Mutex
	loginErr error
	trays    []*FakeTray
	notices  []Notice
	login    bool
	online   []func(bool)
	resume   []func()
}

var _ Desktop = (*Fake)(nil)

// FakeTray is a tray icon that exists only in memory.
type FakeTray struct {
	opts      TrayOptions
	toolTip   string
	menu      []MenuItem
	destroyed bool
	mu        *sync.Mutex
}

// NewTray remembers the icon.
func (f *Fake) NewTray(o TrayOptions) (Tray, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.TrayErr != nil {
		return nil, f.TrayErr
	}
	t := &FakeTray{opts: o, toolTip: o.ToolTip, menu: slices.Clone(o.Menu), mu: &f.mu}
	f.trays = append(f.trays, t)
	return t, nil
}

// Set keeps the tooltip and the menu.
func (t *FakeTray) Set(toolTip string, menu []MenuItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.toolTip, t.menu = toolTip, slices.Clone(menu)
}

// Destroy removes the icon.
func (t *FakeTray) Destroy() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.destroyed = true
}

// Trays are the icons that exist: made and not destroyed.
func (f *Fake) Trays() []*FakeTray {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(f.trays), func(t *FakeTray) bool { return t.destroyed })
}

// ToolTip is the icon's current tooltip.
func (t *FakeTray) ToolTip() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.toolTip
}

// Menu is the icon's current menu.
func (t *FakeTray) Menu() []MenuItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.menu)
}

// Open clicks the icon.
func (t *FakeTray) Open() {
	if t.opts.OnOpen != nil {
		t.opts.OnOpen()
	}
}

// Notify remembers the notification.
func (f *Fake) Notify(n Notice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.NotifyErr != nil {
		return f.NotifyErr
	}
	f.notices = append(f.notices, n)
	return nil
}

// Notices are the notifications shown, in order.
func (f *Fake) Notices() []Notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.notices)
}

// StartsAtLogin reports what SetStartAtLogin last set.
func (f *Fake) StartsAtLogin() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.login
}

// SetStartAtLogin remembers the choice.
func (f *Fake) SetStartAtLogin(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loginErr != nil {
		return f.loginErr
	}
	f.login = on
	return nil
}

// FailLogin makes SetStartAtLogin fail with err, or work again with nil. It may
// be called while the application is running.
func (f *Fake) FailLogin(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginErr = err
}

// OpenedAtLogin returns OpenedByLogin.
func (f *Fake) OpenedAtLogin() bool { return f.OpenedByLogin }

// Online reports the opposite of Offline.
func (f *Fake) Online() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.Offline
}

// OnOnline registers fn to hear ChangeOnline.
func (f *Fake) OnOnline(fn func(bool)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online = append(f.online, fn)
	return func() {}
}

// OnResume registers fn to hear Resume.
func (f *Fake) OnResume(fn func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resume = append(f.resume, fn)
	return func() {}
}

// ChangeOnline changes the connection and tells the listeners.
func (f *Fake) ChangeOnline(online bool) {
	f.mu.Lock()
	f.Offline = !online
	listeners := slices.Clone(f.online)
	f.mu.Unlock()
	for _, fn := range listeners {
		fn(online)
	}
}

// Resume tells the listeners the computer woke.
func (f *Fake) Resume() {
	f.mu.Lock()
	listeners := slices.Clone(f.resume)
	f.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
}
