# Project guidance

- Run `go tool framework dev`, `go tool framework build`, and
  `go tool framework doctor`; these use the pinned Go MyGo tool. Bun is not
  required.
- Run `go test ./...`, `CGO_ENABLED=0 go test ./...`, and
  `go tool mygo vet .` before committing.
- Create windows and Onevra shells inside `mygo.App.WhenReady` and handle
  `App.Run` errors.
- Keep application state outside view callbacks. State shared with other
  goroutines or persisted lives in a `state.Cell` (`main.go` shows the
  pattern): the view reads `Get()` once per build, controls bind to local
  copies and commit with `OnChange` and `Update`, and `Subscribe` plus
  `win.Invalidate` redraws. Subscribers run on the writer's goroutine and
  must not block; wrap main-thread-only APIs in `mygo.RunOnMain`. Plain
  fields are for UI-only values. Use stable data keys for dynamic widgets
  and `ui.Handle` for retained widget identity.
- Use Onevra components and the root shell theme. Do not perform disk or
  network work inside view builders.
- Preferences save automatically through `state.Autosave`. Preserve the
  cancelable close handler: it flushes the saver, and a failed save must
  leave the window open. Close the saver, then the database, after the
  event loop stops. With the tray icon up, the same handler hides the window
  after a successful flush instead of closing it; a quit sets
  `presence.quitting` from `OnBeforeQuit`, so it closes normally. The
  `closers` in `shutdown.go` stop background work, then jobs, then the
  saver, then the database.
- Background behaviour lives in `background`: the scheduler, which versions
  were announced, `Judge` (what may be installed with nobody watching) and
  the `Desktop` seam over MyGo's tray, notifications and start at login.
  Unattended installs set `unattended` on the operation: it asks no
  question, never raises the administrator prompt and never closes a
  program.
- Preferences are typed settings in SQLite (`settings.go`). Add schema
  changes as new Goose migrations in `database/migrations`, numbered after
  the existing ones.
- Run long work with `jobs.Manager` (`selfupdate.go` shows the pattern):
  `Start` returns `jobs.ErrRunning` while a job of that name runs, jobs
  must honor their context, and views read `Status` and redraw from a
  `Subscribe` callback via `Window.Invalidate`. `run` shuts the manager
  down before the database closes.
- This application uses native views and direct Go calls.
- Put user-visible strings in `locales/en.json` and call the generated
  `locales` functions; run `go tool mygo generate .` after editing a
  catalog. Product names stay untranslated.
- Updates are opt-in through the `updates` section in `mygo.json`; never
  commit an update private key. The matching private key stays out of the
  repository and lives in the `MYGO_UPDATER_PRIVATE_KEY` CI secret.
- Errors reach Sentry only through `errorreport` and only when `SENTRY_DSN`
  is set or a DSN was compiled into the build. Report at boundaries
  (`run`, jobs, failed saves); call `errorreport.Flush()` before any new
  exit path. Do not add tracing or replay.
- `resolveDirs` decides where files live: preferences and the database
  under the user configuration directory, downloads, logs and caches under
  the user cache directory. `-data-dir DIR` points everything at one
  directory and skips the single-instance lock, for tests and side-by-side
  development.
