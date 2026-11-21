# Release Sync

Install, update and remove software published as release assets on
code-hosting sites. A native MyGo application with Onevra components and
typed SQLite preferences. Requires Go 1.27.3 and the native libraries
required by MyGo for your platform.

MyGo MCP is included as a pinned dependency.

```sh
go mod download
go tool framework dev
go tool framework doctor
go tool framework build
```

The framework delegates to the pinned `go tool mygo`. You can also use
`go run .`, `go tool mygo dev`, and `go tool mygo build` directly.
No JavaScript runtime or frontend is required.

```sh
go test ./...
CGO_ENABLED=0 go test ./...
go tool mygo vet .
```

Choose a theme in **Settings → Appearance**, close the window, and reopen
it. Preferences live in `release-sync/app.db` under `os.UserConfigDir()`;
run with `-data-dir DIR` to use another directory and skip the
single-instance lock. Startup applies the migrations in
`database/migrations` (add yours after `00001`). A failed preferences save
keeps the window open with a visible error. The database is closed after
the native event loop stops. The shell remembers window bounds under
`main` and opens after its first frame is drawn.

Paste a repository link in the box above the Library to track a GitHub
repository, then press **Check for updates** (or F5) to look for new
releases. The Library, Updates and Activity pages live in the application
database. The GitHub token, saved in **Settings → GitHub**, is kept in the
operating system's credential store and never in the database or an export;
the GitHub CLI's token can be used instead when none is saved. Releases are
fetched with conditional requests, and the request allowance shows in the
sidebar.

Links to GitLab (gitlab.com or self-hosted, nested groups included) and to
Forgejo or Gitea sites (Codeberg or self-hosted) are tracked the same way; a
host that is none of the well-known ones is identified by asking it for the
project. These sites are used without an account. **Import & Export** saves the
Library as a `.json` file or a list of links, and imports a file or pasted
text of repository links, including exports from ReleaseSync and `bin`.

**Settings → Data → Your devices and sharing** keeps the Library, groups,
notes and Try later list the same on every PC: through a private GitHub Gist
(it needs a token that can read and write gists) or through a folder that a
file-sync service shares. The shared file can be locked with a passphrase
(Argon2id and XChaCha20-Poly1305), which each PC keeps in its credential
store. What is installed, settings, tokens and Activity are never shared.

Updates come from this repository's releases through MyGo's signed
updater, configured by `updates` in `mygo.json`. Pushing a `v*` tag runs
`.github/workflows/release.yml`, which builds the Windows amd64 and arm64
installers and attaches them, with the signed update files, to a draft
release. The signing key is the `MYGO_UPDATER_PRIVATE_KEY` secret
(`go tool mygo keygen` makes the pair; the private key is never
committed). A `SENTRY_DSN` secret compiles error reporting into release
builds.
