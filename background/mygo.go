package background

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"sync"

	"github.com/egoist/mygo"
)

// MyGo is the Desktop of the running application, built on MyGo's tray,
// notification, start-at-login, power and network APIs.
type MyGo struct{}

var _ Desktop = MyGo{}

// NewTray adds the icon to the notification area. It must run after the
// application is ready.
func (MyGo) NewTray(o TrayOptions) (Tray, error) {
	native, err := mygo.NewTray(mygo.TrayOptions{ID: "main", Icon: trayIcon(), ToolTip: o.ToolTip})
	if err != nil {
		return nil, fmt.Errorf("add tray icon: %w", err)
	}
	t := &myGoTray{native: native, menu: o.Menu}
	if runtime.GOOS == "linux" {
		// The notification area there only shows a menu, and reports no clicks.
		native.SetMenu(t.nativeMenu())
		return t, nil
	}
	// A click opens the window and a right click shows the menu, as on other
	// Windows apps. A native menu would take the left click too, so it is set
	// only while it pops up.
	if o.OnOpen != nil {
		native.OnClick(o.OnOpen)
	}
	native.OnRightClick(func() {
		native.SetMenu(t.nativeMenu())
		defer native.SetMenu(nil)
		native.PopUpMenu()
	})
	return t, nil
}

type myGoTray struct {
	native *mygo.Tray
	mu     sync.Mutex
	menu   []MenuItem
}

func (t *myGoTray) Set(toolTip string, menu []MenuItem) {
	t.mu.Lock()
	t.menu = menu
	t.mu.Unlock()
	t.native.SetToolTip(toolTip)
	if runtime.GOOS == "linux" {
		t.native.SetMenu(t.nativeMenu())
	}
}

func (t *myGoTray) Destroy() { t.native.Destroy() }

func (t *myGoTray) nativeMenu() *mygo.Menu {
	t.mu.Lock()
	defer t.mu.Unlock()
	items := make([]*mygo.MenuItem, 0, len(t.menu))
	for _, m := range t.menu {
		if m.Label == "" {
			items = append(items, mygo.Separator())
			continue
		}
		items = append(items, &mygo.MenuItem{Label: m.Label, Click: func(*mygo.MenuItem, *mygo.Window) { m.Click() }})
	}
	return mygo.NewMenu(items)
}

// Notify shows the notification and waits for the system to take it.
func (MyGo) Notify(n Notice) error {
	if !mygo.NotificationsSupported() {
		return ErrNotifications
	}
	err := mygo.NewNotification(mygo.NotificationOptions{ID: n.ID, Title: n.Title, Body: n.Body}).Show()
	if errors.Is(err, mygo.ErrNotificationsDenied) {
		return fmt.Errorf("%w: %w", ErrNotifications, err)
	}
	if err != nil {
		return fmt.Errorf("show notification: %w", err)
	}
	return nil
}

// StartsAtLogin reports the system's start-at-login entry.
func (MyGo) StartsAtLogin() bool { return mygo.App.OpenAtLogin() }

// SetStartAtLogin adds or removes the system's start-at-login entry.
func (MyGo) SetStartAtLogin(on bool) error {
	if err := mygo.App.SetOpenAtLogin(on); err != nil {
		return fmt.Errorf("set start at login: %w", err)
	}
	return nil
}

// OpenedAtLogin reports a start made by the system at sign-in.
func (MyGo) OpenedAtLogin() bool { return mygo.App.WasOpenedAtLogin() }

// Online reports the network status.
func (MyGo) Online() bool { return mygo.Network.Status().Online }

// OnOnline calls fn when the connection changes.
func (MyGo) OnOnline(fn func(online bool)) func() {
	return mygo.Network.OnChanged(func(s mygo.NetworkStatus) { fn(s.Online) })
}

// OnResume calls fn when the computer wakes from sleep.
func (MyGo) OnResume(fn func()) func() { return mygo.Power.OnResume(fn) }

// trayIcon draws a blue disc with a white arrow pointing down: something to
// download. It is drawn here rather than shipped so there is no file to keep in
// step with the product's icon.
func trayIcon() []byte {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	blue, white := color.NRGBA{R: 59, G: 110, B: 230, A: 255}, color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for y := range size {
		for x := range size {
			dx, dy := float64(x)-15.5, float64(y)-15.5
			if dx*dx+dy*dy <= 15*15 {
				img.SetNRGBA(x, y, blue)
			}
		}
	}
	for y := 8; y < 24; y++ {
		for x := 12; x < 20; x++ {
			if y < 17 {
				img.SetNRGBA(x, y, white)
			}
		}
		if y >= 15 {
			half := 24 - y
			for x := 16 - half; x < 16+half; x++ {
				img.SetNRGBA(x, y, white)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
