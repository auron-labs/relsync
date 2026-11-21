package sync

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"
)

// Keys say how the shared text is locked.
type Keys struct {
	// Passphrase opens an encrypted sync file, and locks the one written when
	// Encrypt is set.
	Passphrase string
	// Encrypt says the file is to be written encrypted. A file that is
	// encrypted and is written plain, or the other way, is rewritten.
	Encrypt bool
}

// Result is the outcome of a round.
type Result struct {
	// Merged is everything this device and the shared file know.
	Merged Document
	// Changed says the shared file had something this device did not.
	Changed bool
	// Pushed says this device rewrote the shared file.
	Pushed bool
	// FirstSync says there was no shared file yet.
	FirstSync bool
}

// Round is one sync: it reads the shared text, merges it with the local
// document, and writes the result back when it differs from what was there, the
// text's lock changed, or self has not been seen for Heartbeat. A text that
// cannot be read, opened or understood is never overwritten; the error says
// which of ErrUnreadable, ErrNewerFormat, ErrLocked, ErrWrongPassphrase or
// ErrDamaged it was, or is the transport's own.
func Round(ctx context.Context, t Transport, local Document, keys Keys, self Device, now time.Time) (Result, error) {
	if keys.Encrypt {
		if err := ValidatePassphrase(keys.Passphrase); err != nil {
			return Result{}, err
		}
	}
	remote, remoteEncrypted, err := readShared(ctx, t, keys.Passphrase)
	if err != nil {
		return Result{}, err
	}

	self.LastSeen = now.UTC()
	local.Devices = maps.Clone(local.Devices)
	if local.Devices == nil {
		local.Devices = map[string]Device{}
	}
	local.Devices[self.ID] = self
	first := remote == nil
	if first {
		remote = &Document{}
	}
	merged := Merge(local, *remote, now)
	merged.WrittenBy = self.Name

	seen, seenBefore := remote.Devices[self.ID]
	push := first || merged.Signature() != remote.Signature() || remoteEncrypted != keys.Encrypt ||
		!seenBefore || now.Sub(seen.LastSeen) > Heartbeat
	if push {
		if err := writeShared(ctx, t, merged, keys); err != nil {
			return Result{}, err
		}
	}
	return Result{Merged: merged, Changed: merged.Signature() != local.Signature(), Pushed: push, FirstSync: first}, nil
}

// readShared reads and decodes the shared text; nil when there is none.
func readShared(ctx context.Context, t Transport, passphrase string) (doc *Document, encrypted bool, err error) {
	text, found, err := t.Read(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("read the sync file: %w", err)
	}
	if !found || strings.TrimSpace(text) == "" {
		return nil, false, nil
	}
	if encrypted = IsEncrypted(text); encrypted {
		if text, err = Decrypt(text, passphrase); err != nil {
			return nil, true, err
		}
	}
	parsed, err := Parse(text)
	if err != nil {
		return nil, encrypted, err
	}
	return &parsed, encrypted, nil
}

func writeShared(ctx context.Context, t Transport, doc Document, keys Keys) error {
	text, err := doc.Encode()
	if err != nil {
		return err
	}
	if keys.Encrypt {
		if text, err = Encrypt(text, keys.Passphrase); err != nil {
			return err
		}
	}
	if err := t.Write(ctx, text); err != nil {
		return fmt.Errorf("write the sync file: %w", err)
	}
	return nil
}
