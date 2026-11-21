package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// emptyState is a page with nothing to show yet: its title and one sentence
// saying what will appear there.
func (s *screen) emptyState(c *ui.Context, title, sentence string) {
	t := c.Theme()
	ui.Column(c).Fill().Padding(24).AlignItems(ui.Center).Justify(ui.Center).Gap(t.Space(2)).Children(func() {
		if title != "" {
			components.Text(c, title).FontSize(t.Rem(1.25)).Bold()
		}
		components.Text(c, sentence).TextColor(t.TextMuted).Wrap()
	})
}
