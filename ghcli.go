package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ghTimeout bounds how long the GitHub CLI may take to print its token.
const ghTimeout = 5 * time.Second

// githubCLIToken returns the token the GitHub CLI (gh) holds, or "" when gh is
// not installed. An installed gh that is not signed in is an error.
func githubCLIToken(ctx context.Context) (string, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		// Not installed is the ordinary case, not a failure.
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "auth", "token") // #nosec G204 -- fixed arguments to the gh found on PATH.
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read the GitHub CLI token: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
