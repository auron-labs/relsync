package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/secrets"
	secretsmygo "github.com/auron-labs/mygo-framework/secrets/mygo"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/imagecache"
	"github.com/aaronflorey/release-sync/lanes"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo"
)

// avatarDirectory is where downloaded avatars are cached, inside the cache directory.
const avatarDirectory = "avatars"

// services are the application's stores and clients, shared by every page.
type services struct {
	catalog  *catalog.Catalog
	checker  *catalog.Checker
	activity *activity.Log
	// github is the client behind catalog; account and token features use it directly.
	github *source.GitHub
	// sources sends each request to the site its repository is on; the catalog
	// uses it, and it knows how to fetch the release files of every site.
	sources *source.Registry
	// discover builds the Discover lists from GitHub.
	discover *discover.Service
	// searcher finds repositories by name, for the PC scan and relinking.
	searcher source.Searcher
	// tokens is the OS secret store the GitHub token lives in.
	tokens  secrets.SecretStore
	avatars *imagecache.Cache
	// fetcher downloads release files and checksum files.
	fetcher *fetch.Client
	// virusTotal looks file hashes up; its key is the person's own, from tokens.
	virusTotal fetch.VirusTotal
	// lanes holds the downloads of one kind to a few at a time and applies
	// installs one at a time; transfers and installs start through it.
	lanes *lanes.Lanes
	// platform is the operating-system seam, replaceable in tests.
	platform platform.Platform
}

// The lanes jobs run in, and how many of each run at once.
const (
	laneDownload = "download"
	laneApply    = "apply"
	laneCheck    = "check"
	// downloadsAtOnce is how many files are fetched at the same time, and
	// checksAtOnce how many releases the PC scan looks up at the same time.
	downloadsAtOnce = 3
	checksAtOnce    = 3
)

// newServices opens the stores on db and builds the GitHub client.
func newServices(ctx context.Context, db *sql.DB, d dirs, manager *jobs.Manager, tokens secrets.SecretStore) (services, error) {
	github := source.NewGitHub(source.GitHubOptions{UserAgent: userAgent()})
	return openServices(ctx, db, d, manager, github, nil, tokens)
}

// userAgent identifies the application to the sites.
func userAgent() string { return appName + "/" + mygo.App.Version() }

// openServices is newServices with the catalog's Source given, so tests can
// stand in for the sites; nil uses the sources of every site.
func openServices(ctx context.Context, db *sql.DB, d dirs, manager *jobs.Manager, github *source.GitHub, src source.Source, tokens secrets.SecretStore) (services, error) {
	sources := source.NewRegistry(github, source.RegistryOptions{UserAgent: userAgent()})
	if src == nil {
		src = sources
	}
	cat, err := catalog.Open(ctx, db, src, errorText)
	if err != nil {
		return services{}, fmt.Errorf("open catalog: %w", err)
	}
	log, err := activity.Open(ctx, db)
	if err != nil {
		return services{}, fmt.Errorf("open activity log: %w", err)
	}
	return services{
		catalog: cat, checker: catalog.NewChecker(cat, manager), activity: log, github: github, sources: sources, searcher: github, tokens: tokens,
		discover: discover.NewService(discover.Options{Client: github, DB: db, Jobs: manager, Policy: cat.Policy}),
		avatars:  imagecache.New(filepath.Join(d.cache, avatarDirectory), nil, nil),
		fetcher:  fetch.NewClient(fetch.Options{}), virusTotal: fetch.VirusTotal{UserAgent: userAgent()},
		lanes:    lanes.New(manager, map[string]int{laneDownload: downloadsAtOnce, laneApply: 1, laneCheck: checksAtOnce}),
		platform: platform.Current(),
	}, nil
}

// newTokenStore returns the OS credential store, kept apart from the
// development build's.
func newTokenStore() (secrets.SecretStore, error) {
	environment := "production"
	if mygo.IsDev() {
		environment = "development"
	}
	store, err := secretsmygo.New(appSlug, environment)
	if err != nil {
		return nil, fmt.Errorf("open credential store: %w", err)
	}
	return store, nil
}
