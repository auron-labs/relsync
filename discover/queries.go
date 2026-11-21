package discover

import (
	"fmt"
	"strings"
	"time"
)

// Tab is one of the lists on the Discover page.
type Tab string

// The tabs, in the order they are shown.
const (
	Trending Tab = "trending"
	Popular  Tab = "popular"
	New      Tab = "new"
	Updated  Tab = "updated"
	Featured Tab = "featured"
	Stars    Tab = "stars"
)

// Tabs lists every tab in display order.
func Tabs() []Tab { return []Tab{Trending, Popular, New, Updated, Featured, Stars} }

// Valid reports a known tab.
func (t Tab) Valid() bool {
	for _, known := range Tabs() {
		if t == known {
			return true
		}
	}
	return false
}

// Query is one search whose results go into a list.
type Query struct {
	Text    string
	PerPage int
}

const (
	// perTopic is how many results each topic query asks for; Everything's
	// topics overlap the other categories', so it asks for fewer.
	perTopic    = 40
	perTopicAll = 30
	// establishedResults is how many of the busy, long-standing projects a
	// Trending list adds for each of its first topics.
	establishedResults = 20
	establishedTopics  = 2
	// seedChunk is how many curated repositories one search names.
	seedChunk = 6
)

// Queries are the searches that build a tab's list for a category. The list is
// the merge of their results. The Stars tab is not searched for.
func Queries(tab Tab, c Category, now time.Time) []Query {
	if tab == Featured {
		return featuredQueries(c)
	}
	ago := func(days int) string { return now.UTC().AddDate(0, 0, -days).Format(time.DateOnly) }
	var suffix string
	switch tab {
	case Popular:
		suffix = "archived:false stars:>150"
	case New:
		suffix = "archived:false stars:>40 created:>" + ago(60)
	case Updated:
		suffix = "archived:false stars:>200 pushed:>" + ago(7)
	case Trending:
		suffix = "archived:false stars:>40 created:>" + ago(365) + " pushed:>" + ago(14)
	case Featured, Stars:
		return nil
	}
	size := perTopic
	if c.Key == AllKey {
		size = perTopicAll
	}
	queries := make([]Query, 0, len(c.Topics)+establishedTopics)
	for _, topic := range c.Topics {
		queries = append(queries, Query{Text: topic + " " + suffix, PerPage: size})
	}
	if tab == Trending {
		for _, topic := range c.Topics[:min(len(c.Topics), establishedTopics)] {
			queries = append(queries, Query{
				Text: topic + " archived:false stars:>1000 pushed:>" + ago(7), PerPage: establishedResults,
			})
		}
	}
	return queries
}

// featuredQueries name the curated repositories, six to a search.
func featuredQueries(c Category) []Query {
	seeds := seedsOf(c)
	var queries []Query
	for start := 0; start < len(seeds); start += seedChunk {
		chunk := seeds[start:min(len(seeds), start+seedChunk)]
		names := make([]string, len(chunk))
		for i, s := range chunk {
			names[i] = "repo:" + s
		}
		queries = append(queries, Query{Text: strings.Join(names, " "), PerPage: 30})
	}
	return queries
}

// listKey names the cached list of a tab and category.
func listKey(tab Tab, category string) string { return fmt.Sprintf("%s|%s", tab, category) }
