package main

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/auron-labs/mygo-framework/secrets"

	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

const (
	// virusTotalKeyName is the credential-store entry of the person's VirusTotal API key.
	virusTotalKeyName = "virustotal-api-key"
	virusTotalJob     = "virustotal-key"
)

// virusTotalState is the VirusTotal key as Settings shows it. It is only
// touched on the UI thread.
type virusTotalState struct {
	// input is the key being typed, and note the outcome of the last attempt.
	input, note string
	// saved says a key is in the credential store.
	saved bool
}

// virusTotalLookup is the lookup of a file's hash with the saved key. It is
// nil when the check is off or there is no key, so nothing is sent.
func (s *screen) virusTotalLookup(ctx context.Context, on bool) fetch.VirusTotalLookup {
	if !on {
		return nil
	}
	key, err := s.tokens.Get(ctx, virusTotalKeyName)
	if err != nil || len(key) == 0 {
		if err != nil && !errors.Is(err, secrets.ErrNotFound) {
			// No credential store, or it is locked: the lookup is skipped.
			log.Printf("read the VirusTotal API key: %v", err)
		}
		return nil
	}
	apiKey := string(key)
	return func(ctx context.Context, sum string) (fetch.VirusTotalReport, error) {
		return s.virusTotal.Lookup(ctx, apiKey, sum)
	}
}

// refreshVirusTotalKey finds out whether a key is saved, in the background.
func (s *screen) refreshVirusTotalKey() {
	s.startJob(virusTotalJob, func(ctx context.Context) error {
		key, err := s.tokens.Get(ctx, virusTotalKeyName)
		if err != nil && !errors.Is(err, secrets.ErrNotFound) {
			log.Printf("read the VirusTotal API key: %v", err)
		}
		saved := err == nil && len(key) > 0
		s.onUI(func() { s.vtKey.saved = saved })
		return nil
	})
}

// saveVirusTotalKey keeps the key typed in Settings in the credential store.
func (s *screen) saveVirusTotalKey() {
	key := strings.TrimSpace(s.vtKey.input)
	if key == "" {
		return
	}
	s.startJob(virusTotalJob, func(ctx context.Context) error {
		if err := s.tokens.Set(ctx, virusTotalKeyName, []byte(key)); err != nil {
			log.Printf("save the VirusTotal API key: %v", err)
			note := locales.VirusTotalKeySaveFailed(err.Error())
			s.onUI(func() { s.vtKey.note = note })
			return nil
		}
		s.onUI(func() { s.vtKey.input, s.vtKey.saved, s.vtKey.note = "", true, locales.VirusTotalKeySaved() })
		return nil
	})
}

// removeVirusTotalKey deletes the saved key.
func (s *screen) removeVirusTotalKey() {
	s.startJob(virusTotalJob, func(ctx context.Context) error {
		if err := s.tokens.Delete(ctx, virusTotalKeyName); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			log.Printf("remove the VirusTotal API key: %v", err)
			note := locales.VirusTotalKeySaveFailed(err.Error())
			s.onUI(func() { s.vtKey.note = note })
			return nil
		}
		s.onUI(func() { s.vtKey.saved, s.vtKey.note = false, locales.VirusTotalKeyRemoved() })
		return nil
	})
}

// virusTotalKeyControl is the key's field with its buttons and the outcome of
// the last attempt.
func (s *screen) virusTotalKeyControl(c *ui.Context) {
	t := c.Theme()
	st := &s.vtKey
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.TextBox(c, &st.input, components.TextBoxOptions{LeadingIcon: lucide.KeyRound()}).
			Label(locales.VirusTotalKeyLabel()).Placeholder(locales.VirusTotalKeyPlaceholder()).Password().
			FillWidth().MaxWidth(tokenFieldWidth)
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			save := components.Button(c, locales.VirusTotalKeySave(), components.ButtonPrimary).Key("vt-save").Disabled(strings.TrimSpace(st.input) == "")
			if save.Clicked() {
				s.saveVirusTotalKey()
			}
			if st.saved {
				if components.Button(c, locales.VirusTotalKeyRemove(), components.ButtonOutline).Key("vt-remove").Clicked() {
					s.removeVirusTotalKey()
				}
			}
		})
		switch {
		case st.note != "":
			components.Text(c, st.note).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
		case st.saved:
			components.Text(c, locales.VirusTotalKeyKept()).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
		}
	})
}

// securitySettingsGroups holds the Security section: what is checked before a
// download is used, and the hints shown before a repository is added.
func (s *screen) securitySettingsGroups() []components.SettingsGroup {
	p := s.prefs.Get()
	verify, scan, provenance, virusTotal, hints := p.VerifyDownloads, p.ScanDownloads, p.CheckProvenance, p.UseVirusTotal, p.SafetyHints
	toggle := func(id, title, hint, label string, value *bool, current bool, set func(p *preferences, on bool)) components.SettingsItem {
		return components.SettingsItem{
			ID: id, Title: title, Description: hint,
			Build: func(c *ui.Context) {
				components.Switch(c, value, label, components.SwitchOptions{HideLabel: true})
				if *value != current {
					on := *value
					s.prefs.Update(func(p *preferences) { set(p, on) })
				}
			},
		}
	}
	return []components.SettingsGroup{{ID: "verify", Items: []components.SettingsItem{
		toggle("verify-downloads", locales.VerifyDownloads(), locales.VerifyDownloadsHint(), locales.VerifyDownloadsLabel(),
			&verify, p.VerifyDownloads, func(p *preferences, on bool) { p.VerifyDownloads = on }),
		toggle("scan-downloads", locales.ScanDownloads(), locales.ScanDownloadsHint(), locales.ScanDownloadsLabel(),
			&scan, p.ScanDownloads, func(p *preferences, on bool) { p.ScanDownloads = on }),
		toggle("check-provenance", locales.CheckProvenance(), locales.CheckProvenanceHint(), locales.CheckProvenanceLabel(),
			&provenance, p.CheckProvenance, func(p *preferences, on bool) { p.CheckProvenance = on }),
		toggle("safety-hints", locales.SafetyHints(), locales.SafetyHintsHint(), locales.SafetyHintsLabel(),
			&hints, p.SafetyHints, func(p *preferences, on bool) { p.SafetyHints = on }),
	}}, {ID: "virustotal", Title: locales.VirusTotalTitle(), Items: []components.SettingsItem{
		toggle("use-virustotal", locales.UseVirusTotal(), locales.UseVirusTotalHint(), locales.UseVirusTotalLabel(),
			&virusTotal, p.UseVirusTotal, func(p *preferences, on bool) { p.UseVirusTotal = on }),
		{
			ID: "virustotal-key", Title: locales.VirusTotalKeyTitle(), Description: locales.VirusTotalKeyHint(), Stacked: true,
			Keywords: []string{"virustotal", "api key", "antivirus"},
			Build:    s.virusTotalKeyControl,
		},
	}}}
}
