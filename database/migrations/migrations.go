// Package migrations embeds the application's SQL migration history.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded SQL migrations.
func FS() fs.FS {
	return files
}
