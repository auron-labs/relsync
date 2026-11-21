package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// errDeclined is the person's answer of no to a question, or dismissing it.
var errDeclined = errors.New("the person declined")

// question is something an operation asks before it goes on. Cancel is always
// offered.
type question struct {
	title string
	// lines are paragraphs under the title; warnings follow them in a warning colour.
	lines    []string
	warnings []string
	// confirm labels the button that says go ahead.
	confirm string
	// tick labels a checkbox that starts ticked, or unticked with tickOff; empty
	// has none.
	tick    string
	tickOff bool
	// choices are programs to pick from, of which those in ticked start ticked.
	// At least one must be ticked to go ahead.
	choices []string
	ticked  []string
	// unattended is why an unattended operation stops here instead of asking;
	// unset, it is that the Item needs attention.
	unattended background.Reason
}

// answer is the reply to a question that was confirmed.
type answer struct {
	// ticked is the state of the checkbox.
	ticked bool
	// chosen are the choices left ticked, in the order they were listed.
	chosen []string
}

// ask shows a question and waits for the reply. Questions are shown one at a
// time, as the dialog host has room for one. It returns errDeclined when the
// person cancels or dismisses it, and the context's error when the operation
// is canceled while it waits.
func (s *screen) ask(ctx context.Context, q question) (answer, error) {
	select {
	case s.asking <- struct{}{}:
	case <-ctx.Done():
		return answer{}, fmt.Errorf("ask %q: %w", q.title, context.Cause(ctx))
	}
	defer func() { <-s.asking }()

	replies := make(chan *answer, 1)
	var once sync.Once
	reply := func(a *answer) { once.Do(func() { replies <- a }) }
	// Both closures run on the UI thread, one after the other.
	var shown *appDialog
	s.onUI(func() { shown = s.showQuestion(q, reply) })

	select {
	case a := <-replies:
		if a == nil {
			return answer{}, errDeclined
		}
		return *a, nil
	case <-ctx.Done():
		// Take the question off the screen: nobody can answer it now.
		s.onUI(func() { s.dismissDialog(shown) })
		return answer{}, fmt.Errorf("ask %q: %w", q.title, context.Cause(ctx))
	}
}

// questionState is what the dialog remembers between frames.
type questionState struct {
	ticked bool
	picks  []bool
}

func (s *screen) showQuestion(q question, reply func(*answer)) *appDialog {
	st := &questionState{ticked: q.tick != "" && !q.tickOff, picks: make([]bool, len(q.choices))}
	for i, choice := range q.choices {
		st.picks[i] = slices.Contains(q.ticked, choice)
	}
	d := s.showDialog(q.title, func(c *ui.Context) { s.questionView(c, q, st, reply) })
	d.onDismiss = func() { reply(nil) }
	return d
}

func (s *screen) questionView(c *ui.Context, q question, st *questionState, reply func(*answer)) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(3)).MinWidth(380).MaxWidth(520).Children(func() {
		components.Text(c, q.title).Bold().FontSize(t.Rem(1.1)).Wrap()
		for _, line := range q.lines {
			components.Text(c, line).TextColor(t.TextMuted).Wrap()
		}
		for _, w := range q.warnings {
			components.Text(c, w).TextColor(t.Warning).Wrap()
		}
		for i, choice := range q.choices {
			components.Checkbox(c.Key(strconv.Itoa(i)), &st.picks[i], choice)
		}
		if q.tick != "" {
			components.Checkbox(c, &st.ticked, q.tick)
		}
		ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
			if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
				s.dialogOpen = false
			}
			none := len(q.choices) > 0 && !slices.Contains(st.picks, true)
			if components.Button(c, q.confirm, components.ButtonPrimary).Disabled(none).Clicked() {
				a := answer{ticked: st.ticked}
				for i, choice := range q.choices {
					if st.picks[i] {
						a.chosen = append(a.chosen, choice)
					}
				}
				reply(&a)
				s.dialogOpen = false
			}
		})
	})
}
