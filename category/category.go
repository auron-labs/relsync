// Package category sorts a repository into the kind of software it is, from
// its name, description and topics. The Library groups its Items by it.
package category

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

//go:embed data/categories.json
var data embed.FS

// Key names a category. Keys are stored with Items, so they never change; the
// words people read for them are the interface's.
type Key string

// The categories, in the order that decides a tie: the more specific first.
const (
	Games         Key = "games"
	Media         Key = "media"
	Graphics      Key = "graphics"
	Productivity  Key = "productivity"
	Security      Key = "security"
	Internet      Key = "internet"
	Communication Key = "communication"
	AI            Key = "ai"
	System        Key = "system"
	Developer     Key = "developer"
	// Other is where a repository goes when nothing scores enough.
	Other Key = "other"
)

// The points a word is worth, by where it is found and how telling it is.
const (
	strongTopic, strongName, strongDescription = 4.0, 3.0, 2.0
	weakTopic, weakName, weakDescription       = 2.0, 1.5, 0.75
	// fragmentInName is the worth of a listed piece of a name found anywhere in it.
	fragmentInName = 4.0
	// threshold is the least total a category needs to claim a repository.
	threshold = 2.0
	// tieMargin keeps a later category from taking the place of an earlier one
	// it matches to the point.
	tieMargin = 0.01
)

// rule is the evidence for one category.
type rule struct {
	Key Key `json:"key"`
	// Strong and Weak are words, or phrases of several, that suggest the
	// category.
	Strong []string `json:"strong"`
	Weak   []string `json:"weak"`
	// NameFragments are pieces of a name that suggest it by themselves.
	NameFragments []string `json:"nameFragments"`
	// Weight multiplies the total; zero means 1. General categories carry less
	// so that the specific ones win over them.
	Weight float64 `json:"weight"`
}

var rules = sync.OnceValue(func() []rule {
	raw, err := data.ReadFile("data/categories.json")
	if err != nil {
		panic("category: " + err.Error())
	}
	var list []rule
	// The file ships with the program, so one that does not parse is a bug the
	// tests catch.
	if err := json.Unmarshal(raw, &list); err != nil {
		panic("category: " + err.Error())
	}
	return list
})

// All lists every category, in the order a tie is decided, with Other last.
func All() []Key {
	list := rules()
	keys := make([]Key, 0, len(list)+1)
	for _, r := range list {
		keys = append(keys, r.Key)
	}
	return append(keys, Other)
}

// Of returns the category of a repository, or Other when no category scores
// enough.
func Of(name, description string, topics []string) Key {
	return choose(rules(), newEvidence(name, description, topics))
}

// evidence is a repository's name, description and topics broken into the
// pieces rules look for.
type evidence struct {
	topics map[string]bool
	// nameWords are the words of the name, camel case split; nameLetters is
	// the name's letters and digits run together.
	nameWords   map[string]bool
	nameLetters string
	// nameLower is the name as written, lower-cased, for fragments that depend
	// on the punctuation around them.
	nameLower string
	// description is lower-cased; descriptionWords are its words.
	description      string
	descriptionWords map[string]bool
}

func newEvidence(name, description string, topics []string) evidence {
	ev := evidence{
		topics:      map[string]bool{},
		nameWords:   set(strings.FieldsFunc(strings.ToLower(splitCamelCase(name)), func(r rune) bool { return !isASCIIWordRune(r) })),
		description: strings.ToLower(description),
		nameLower:   strings.ToLower(name),
	}
	for _, topic := range topics {
		topic = strings.ToLower(topic)
		ev.topics[topic] = true
		for part := range strings.SplitSeq(topic, "-") {
			ev.topics[part] = true
		}
	}
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			ev.nameLetters += string(r)
		}
	}
	ev.descriptionWords = set(strings.FieldsFunc(ev.description, func(r rune) bool { return !isASCIIWordRune(r) && r != '-' && r != '+' }))
	return ev
}

func set(words []string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}

func isASCIIWordRune(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' }

// splitCamelCase puts a space where a lower-case letter or digit is followed
// by a capital: "RetroArch" becomes "Retro Arch".
func splitCamelCase(name string) string {
	var b strings.Builder
	var previous rune
	for _, r := range name {
		if unicode.IsUpper(r) && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
		previous = r
	}
	return b.String()
}

// score is what a rule's evidence adds up to for a repository.
func (r rule) score(ev evidence) float64 {
	var total float64
	for _, word := range r.Strong {
		if ev.topics[word] {
			total += strongTopic
		}
		if ev.nameWords[word] {
			total += strongName
		}
		// A phrase has to be found whole; a single word has to be a word.
		phrase := strings.ContainsAny(word, " -") && strings.Contains(ev.description, word)
		if ev.descriptionWords[word] || phrase {
			total += strongDescription
		}
	}
	for _, word := range r.Weak {
		if ev.topics[word] {
			total += weakTopic
		}
		if ev.nameWords[word] {
			total += weakName
		}
		if ev.descriptionWords[word] {
			total += weakDescription
		}
	}
	for _, fragment := range r.NameFragments {
		if ev.hasFragment(fragment) {
			total += fragmentInName
		}
	}
	if r.Weight != 0 {
		total *= r.Weight
	}
	return total
}

// hasFragment reports a listed piece of a name found in the name. A fragment
// with a hyphen is a word of the name set off by it, such as "ai-", and has to
// be found with the hyphen, at the start of a word: "plain" does not have it.
func (ev evidence) hasFragment(fragment string) bool {
	if !strings.Contains(fragment, "-") {
		return strings.Contains(ev.nameLetters, fragment)
	}
	for from := 0; ; {
		i := strings.Index(ev.nameLower[from:], fragment)
		if i < 0 {
			return false
		}
		start := from + i
		if previous, _ := utf8.DecodeLastRuneInString(ev.nameLower[:start]); start == 0 || !isLetterOrDigit(previous) {
			return true
		}
		from = start + 1
	}
}

func isLetterOrDigit(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// choose picks the highest scoring rule. A rule listed earlier keeps a tie, and
// a best total under the threshold is Other.
func choose(list []rule, ev evidence) Key {
	best, bestScore := Other, 0.0
	for _, r := range list {
		if s := r.score(ev); s > bestScore+tieMargin {
			best, bestScore = r.Key, s
		}
	}
	if bestScore < threshold {
		return Other
	}
	return best
}
