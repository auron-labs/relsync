package main

import (
	"strings"
	"testing"
)

func TestNotesPreviewFlattensMarkdown(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"headings and bullets", "## What's new\n\n- Faster start\n* Fewer crashes\n", "What's new\n• Faster start\n• Fewer crashes"},
		{"links and images", "See [the guide](https://x.test/g) ![shot](https://x.test/s.png) now", "See the guide  now"},
		{"emphasis and code", "**Bold**, __also__, `code` and ~~gone~~", "Bold, also, code and gone"},
		{"html and rules", "<p>Hello</p>\n---\nBye", "Hello\nBye"},
		{"windows line endings", "a\r\n\r\n\r\nb", "a\nb"},
		{"empty", "  \n ", ""},
	} {
		if got := notesPreview(c.in, 1000); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNotesPreviewIsCutOnACharacterBoundary(t *testing.T) {
	got := notesPreview(strings.Repeat("é", 200), 11)
	if !strings.HasSuffix(got, "…") || strings.ToValidUTF8(got, "?") != got {
		t.Errorf("preview = %q", got)
	}
	if len(got) > 11+len("…") {
		t.Errorf("preview is %d bytes", len(got))
	}
}
