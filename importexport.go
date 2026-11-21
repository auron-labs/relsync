package main

import (
	"strings"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

// importUI is what the Import & Export page remembers between frames. It is
// only touched on the UI thread.
type importUI struct {
	// text is the pasted text; fileName and fileText are the file chosen, which
	// is imported instead of the text.
	text, fileName, fileText string
	// group is the option value of the group chosen for everything imported.
	group            string
	alreadyInstalled bool
	installAfter     bool
	// reading says a file is being read.
	reading bool
	// result is the outcome of the last import, and problem why a file or
	// export could not be used.
	result, problem string
}

func newImportUI() importUI { return importUI{} }

// source is the text an import would read: the file's, else what was pasted.
func (p importUI) source() string {
	if p.fileText != "" {
		return p.fileText
	}
	return p.text
}

// importView is the Import & Export page: the card that exports the Library
// and the card that imports a list of repositories.
func (s *screen) importView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(0, pagePadding, pagePadding, pagePadding).Gap(t.Space(4)).Children(func() {
			s.exportCard(c)
			s.importCard(c)
		})
	})
}

// cardHeader is the icon, title and sentence at the top of a card.
func cardHeader(c *ui.Context, icon *ui.SVG, title, sentence string) {
	t := c.Theme()
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
		components.Icon(c, icon).Size(t.Rem(1.75), t.Rem(1.75)).TextColor(t.Accent).Shrink(0)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(t.Space(1)).Children(func() {
			components.Text(c, title).Bold()
			components.Text(c, sentence).TextColor(t.TextMuted).Wrap()
		})
	})
}

// exportCard writes the Library out as a file or a list of links.
func (s *screen) exportCard(c *ui.Context) {
	t := c.Theme()
	count := len(s.catalog.Entries().Get())
	ui.Column(c).FillWidth().Padding(t.Space(4)).Gap(t.Space(3)).Radius(t.Radius).Border(1, t.Border).Children(func() {
		cardHeader(c, lucide.FileDown(), locales.ExportTitle(), locales.ExportHint())
		if count == 0 {
			components.Text(c, locales.ExportEmpty()).TextColor(t.TextMuted).Wrap()
			return
		}
		ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
			if components.Button(c, locales.ExportJson(), components.ButtonPrimary).Key("export-json").Clicked() {
				s.exportLibraryFile()
			}
			if components.Button(c, locales.ExportLinks(), components.ButtonOutline).Key("export-links").Clicked() {
				s.exportLinkFile()
			}
			if components.Button(c, locales.ExportCopy(), components.ButtonOutline).Key("export-copy").Clicked() {
				s.copyLinks()
			}
		})
	})
}

// importCard reads a list of repositories from a file or pasted text and adds
// what is in it.
func (s *screen) importCard(c *ui.Context) {
	t := c.Theme()
	p := &s.importPage
	status, started := s.jobs.Status(importJob)
	running := started && status.State == jobs.Running
	ui.Column(c).FillWidth().Padding(t.Space(4)).Gap(t.Space(3)).Radius(t.Radius).Border(1, t.Border).Children(func() {
		cardHeader(c, lucide.FileUp(), locales.ImportTitle(), locales.ImportHint(appName))
		s.importDropZone(c, running)
		components.TextBox(c, &p.text, components.TextBoxOptions{Multiline: true}).
			Label(locales.ImportPasteLabel()).Placeholder(locales.ImportPastePlaceholder()).FillWidth().Lines(4, 8).
			Disabled(running)
		s.importOptions(c, running)
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
			start := components.Button(c, locales.ImportStart(), components.ButtonPrimary).Key("import-start").
				Disabled(running || p.reading || strings.TrimSpace(p.source()) == "")
			if start.Clicked() {
				s.startImport()
			}
			if running {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Key("import-cancel").Clicked() {
					s.jobs.Cancel(importJob)
				}
			}
		})
		switch {
		case running:
			components.Progress(c, status.Fraction()).Label(locales.ImportTitle())
			components.Text(c, status.Message).TextColor(t.TextMuted).Wrap()
		case p.problem != "":
			components.Text(c, p.problem).TextColor(t.Danger).Wrap()
		case p.result != "":
			components.Text(c, p.result).TextColor(t.TextMuted).Wrap()
		}
	})
}

// importDropZone takes a file dropped on it, or chosen with the button, and
// shows the one that is loaded.
func (s *screen) importDropZone(c *ui.Context, running bool) {
	t := c.Theme()
	p := &s.importPage
	border := t.Border
	zone := ui.Row(c).Key("import-drop").FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).
		Padding(t.Space(3)).Radius(t.Radius)
	if zone.FileDragOver() {
		border = t.Accent
	}
	zone.Border(1, border)
	if files := zone.DroppedFiles(); len(files) > 0 && !running {
		s.loadImportFile(files[0])
	}
	zone.Children(func() {
		text := locales.ImportDropHint()
		if p.fileName != "" {
			text = locales.ImportFileLoaded(p.fileName)
		}
		components.Text(c, text).TextColor(t.TextMuted).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
		if p.fileName != "" {
			if components.Button(c, locales.ImportFileClear(), components.ButtonGhost).Key("import-clear").Disabled(running).Clicked() {
				p.fileName, p.fileText = "", ""
			}
		}
		if components.Button(c, locales.ImportBrowse(), components.ButtonSecondary).Key("import-browse").Disabled(running).Clicked() {
			s.browseImportFile()
		}
	})
}

// importOptions are the choices that apply to everything imported.
func (s *screen) importOptions(c *ui.Context, running bool) {
	t := c.Theme()
	p := &s.importPage
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		labelled(c, locales.ImportGroup(), func() {
			choice, changed := dropdown(c, locales.ImportGroupLabel(), p.group, s.groupOptions())
			if changed {
				p.group = choice
			}
		})
		components.Checkbox(c, &p.alreadyInstalled, locales.ImportAlreadyInstalled()).Disabled(running)
		if !p.alreadyInstalled {
			components.Checkbox(c, &p.installAfter, locales.ImportInstallAfter()).Disabled(running)
		}
	})
}
