// Package discover builds the lists on the Discover page: queries per tab and
// category, merged, filtered and cached results, rankings and their movement,
// the look-up of which projects have a download for this machine, and the
// budgeting of GitHub requests that all of it spends.
package discover

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed data/*.json
var data embed.FS

// AllKey is the category with every other one in it.
const AllKey = "all"

// Category is a kind of software: the topic queries that find its projects and
// a hand-picked list of well-known ones.
type Category struct {
	Key string `json:"key"`
	// Topics are search qualifiers, one query each.
	Topics []string `json:"topics"`
	// Seeds are owner/repo names of curated projects.
	Seeds []string `json:"seeds"`
}

var categories = sync.OnceValue(func() []Category {
	var list []Category
	load("data/categories.json", &list)
	return list
})

// load reads an embedded data file. The files ship with the program, so one
// that does not parse is a bug caught by the tests.
func load(name string, into any) {
	raw, err := data.ReadFile(name)
	if err != nil {
		panic("discover: " + err.Error())
	}
	if err := json.Unmarshal(raw, into); err != nil {
		panic("discover: " + name + ": " + err.Error())
	}
}

// Categories lists every category, Everything first.
func Categories() []Category { return categories() }

// Find returns the category with the key, or Everything when there is none.
func Find(key string) Category {
	for _, c := range categories() {
		if c.Key == key {
			return c
		}
	}
	return categories()[0]
}

// seedsOf is the curated list shown for a category. Everything shows the first
// few of each category's.
func seedsOf(c Category) []string {
	if c.Key != AllKey {
		return c.Seeds
	}
	const perCategory = 5
	var seeds []string
	seen := map[string]bool{}
	for _, other := range categories() {
		for _, s := range other.Seeds[:min(len(other.Seeds), perCategory)] {
			if !seen[strings.ToLower(s)] {
				seen[strings.ToLower(s)] = true
				seeds = append(seeds, s)
			}
		}
	}
	return seeds
}

// CategoryOf names the category a repository belongs to: the one that lists it
// among its curated projects, else the one whose topics it shares the most
// topics with. It reports false when there is none.
func CategoryOf(owner, repo string, topics []string) (key string, ok bool) {
	full := strings.ToLower(owner + "/" + repo)
	for _, c := range categories() {
		if c.Key == AllKey {
			continue
		}
		for _, seed := range c.Seeds {
			if strings.ToLower(seed) == full {
				return c.Key, true
			}
		}
	}
	best := 0
	for _, c := range categories() {
		if c.Key == AllKey {
			continue
		}
		if n := sharedTopics(c, topics); n > best {
			key, best = c.Key, n
		}
	}
	return key, best > 0
}

// sharedTopics counts the repository's topics that the category searches for.
// "windows" narrows most queries without saying what the software is.
func sharedTopics(c Category, topics []string) int {
	wanted := map[string]bool{}
	for _, query := range c.Topics {
		for _, word := range strings.Fields(query) {
			if topic, found := strings.CutPrefix(word, "topic:"); found && topic != "windows" {
				wanted[topic] = true
			}
		}
	}
	n := 0
	for _, t := range topics {
		if wanted[strings.ToLower(t)] {
			n++
		}
	}
	return n
}
