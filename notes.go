package main

import (
	"regexp"
	"strings"
)

var (
	markdownImage  = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	markdownLink   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTag        = regexp.MustCompile(`<[^>]+>`)
	markdownHeader = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s*`)
	markdownBullet = regexp.MustCompile(`(?m)^\s*[-*+]\s+`)
	markdownRule   = regexp.MustCompile(`(?m)^\s*[-*_]{3,}\s*$`)
	blankLines     = regexp.MustCompile(`\n{2,}`)
)

// notesPreview flattens release notes to plain text of at most limit bytes, for
// a row that has no room to render Markdown.
func notesPreview(markdown string, limit int) string {
	text := strings.ReplaceAll(markdown, "\r", "")
	text = markdownImage.ReplaceAllString(text, "")
	text = markdownLink.ReplaceAllString(text, "$1")
	text = htmlTag.ReplaceAllString(text, "")
	text = markdownHeader.ReplaceAllString(text, "")
	text = markdownBullet.ReplaceAllString(text, "• ")
	text = markdownRule.ReplaceAllString(text, "")
	text = strings.NewReplacer("**", "", "__", "", "`", "", "~~", "").Replace(text)
	text = strings.TrimSpace(blankLines.ReplaceAllString(text, "\n"))
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !isRuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
