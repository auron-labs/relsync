package main

import (
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// drawer is the right-hand detail panel; openDrawer fills and opens it.
type drawer struct {
	open  bool
	title string
	view  func(*ui.Context)
}

// appDialog is the one modal dialog the host shows at a time.
type appDialog struct {
	label string
	view  func(*ui.Context)
	// onDismiss runs once the dialog has gone, however it went.
	onDismiss func()
}

// openDrawer shows title and view in the detail drawer.
func (s *screen) openDrawer(title string, view func(*ui.Context)) {
	s.drawer = drawer{open: true, title: title, view: view}
	if s.shell != nil {
		s.shell.SetRightExpanded(true)
	}
	if s.win != nil {
		s.win.Invalidate()
	}
}

// closeTopOverlay closes the top-most overlay the window level still hears:
// the drawer. Toasts and dialogs take Escape first, through their own layers.
func (s *screen) closeTopOverlay() {
	if s.drawer.open {
		s.drawer.open = false
		if s.shell != nil {
			s.shell.SetRightExpanded(false)
		}
	}
}

// drawerView builds the drawer's content, or its empty state.
func (s *screen) drawerView(c *ui.Context) {
	if s.drawer.view != nil {
		s.drawer.view(c)
		return
	}
	s.emptyState(c, locales.DrawerEmptyTitle(), locales.DrawerEmpty())
}

func (s *screen) drawerTitle(c *ui.Context) {
	title := s.drawer.title
	if title == "" {
		title = locales.DrawerEmptyTitle()
	}
	components.Text(c, title).Bold().SingleLine()
}

// showDialog opens view as the modal dialog named label, in place of one that
// is showing. It returns the dialog.
func (s *screen) showDialog(label string, view func(*ui.Context)) *appDialog {
	if s.dialog != nil && s.dialog.onDismiss != nil {
		s.dialog.onDismiss()
	}
	d := &appDialog{label: label, view: view}
	s.dialog = d
	s.dialogOpen = true
	if s.win != nil {
		s.win.Invalidate()
	}
	return d
}

// dismissDialog closes d if it is the dialog showing.
func (s *screen) dismissDialog(d *appDialog) {
	if s.dialog == d {
		s.dialogOpen = false
	}
}

// dialogHost renders the modal dialog every frame. Dialog owns the dim,
// backdrop dismissal and Escape; dismissing clears the dialog for the next
// frame.
func (s *screen) dialogHost(c *ui.Context) {
	if s.dialog == nil {
		s.dialogOpen = false
		components.Dialog(c, &s.dialogOpen, components.DialogOptions{}, nil)
		return
	}
	components.Dialog(c, &s.dialogOpen, components.DialogOptions{Label: s.dialog.label},
		func(ui.Element) { s.dialog.view(c) })
	if !s.dialogOpen {
		gone := s.dialog
		s.dialog = nil
		if gone.onDismiss != nil {
			gone.onDismiss()
		}
	}
}

// queueToast shows message on the next frame. AddToast is only callable while
// the view builds, so code off the UI thread queues here through Window.Update.
func (s *screen) queueToast(toast ui.Toast) {
	s.toasts = append(s.toasts, toast)
	if s.win != nil {
		s.win.Invalidate()
	}
}

func (s *screen) drainToasts(c *ui.Context) {
	for _, toast := range s.toasts {
		c.AddToast(toast)
	}
	s.toasts = s.toasts[:0]
}
