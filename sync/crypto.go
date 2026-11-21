package sync

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// EncryptedFormat marks the envelope an encrypted sync file is wrapped in.
	EncryptedFormat  = "release-sync-library-sync-encrypted"
	encryptedVersion = 1

	kdfName    = "argon2id"
	cipherName = "xchacha20poly1305"

	// MinPassphrase is the fewest characters a passphrase may have.
	MinPassphrase = 8

	// The parameters new files are written with: Argon2id as RFC 9106 gives
	// for memory-constrained settings (64 MiB, three passes, four lanes), which
	// takes a desktop a fraction of a second. They are recorded in the
	// envelope, so a later release can raise them and still open older files.
	defaultTime    = 3
	defaultMemory  = 64 * 1024 // KiB
	defaultThreads = 4

	saltSize = 16
	keySize  = chacha20poly1305.KeySize

	// What an envelope may ask of the machine opening it. The file comes from
	// a place other people's software can write to, so its cost is bounded.
	maxTime    = 10
	maxMemory  = 512 * 1024 // KiB
	maxThreads = 16
)

// The errors of an encrypted file.
var (
	// ErrLocked means the file is encrypted and there is no passphrase.
	ErrLocked = errors.New("this sync file is locked: enter the passphrase you set up on your other device")
	// ErrWrongPassphrase means the passphrase does not open the file, or the
	// file was changed after it was written.
	ErrWrongPassphrase = errors.New("that passphrase doesn't open the sync file")
	// ErrDamaged means the encrypted file is not a well-formed envelope.
	ErrDamaged = errors.New("the sync file is damaged")
	// ErrPassphraseShort means a passphrase has fewer than MinPassphrase characters.
	ErrPassphraseShort = errors.New("a passphrase needs at least 8 characters")
)

// ValidatePassphrase reports ErrPassphraseShort for a passphrase that is too short.
func ValidatePassphrase(passphrase string) error {
	if utf8.RuneCountInString(passphrase) < MinPassphrase {
		return ErrPassphraseShort
	}
	return nil
}

// envelope is the file an encrypted document is kept in. The parameters of the
// key derivation are part of it, and of what the cipher authenticates.
type envelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	KDF     string `json:"kdf"`
	Time    uint32 `json:"time"`
	// Memory is in KiB.
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	Salt    []byte `json:"salt"`
	Cipher  string `json:"cipher"`
	Nonce   []byte `json:"nonce"`
	// Data is the encrypted document followed by the cipher's tag.
	Data []byte `json:"data"`
}

// IsEncrypted reports that text is an encrypted envelope rather than a
// document. Only the format marker at the top level counts; a document that
// mentions it in a note is not mistaken for one.
func IsEncrypted(text string) bool {
	var head struct {
		Format string `json:"format"`
	}
	return json.Unmarshal([]byte(text), &head) == nil && head.Format == EncryptedFormat
}

// Encrypt seals plain with a key derived from the passphrase, with a new salt
// and nonce each time.
func Encrypt(plain, passphrase string) (string, error) {
	e := envelope{
		Format: EncryptedFormat, Version: encryptedVersion, KDF: kdfName, Cipher: cipherName,
		Time: defaultTime, Memory: defaultMemory, Threads: defaultThreads,
		Salt: make([]byte, saltSize), Nonce: make([]byte, chacha20poly1305.NonceSizeX),
	}
	if _, err := rand.Read(e.Salt); err != nil {
		return "", fmt.Errorf("make a salt: %w", err)
	}
	if _, err := rand.Read(e.Nonce); err != nil {
		return "", fmt.Errorf("make a nonce: %w", err)
	}
	aead, err := e.aead(passphrase)
	if err != nil {
		return "", err
	}
	e.Data = aead.Seal(nil, e.Nonce, []byte(plain), e.header())
	encoded, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode the encrypted sync file: %w", err)
	}
	return string(encoded) + "\n", nil
}

// Decrypt opens an encrypted file. It returns ErrLocked without a passphrase,
// ErrWrongPassphrase when the passphrase does not open it or it was altered,
// ErrDamaged for an envelope that is not well formed and ErrNewerFormat for
// one of a newer version.
func Decrypt(text, passphrase string) (string, error) {
	var e envelope
	if err := json.Unmarshal([]byte(text), &e); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDamaged, err)
	}
	if e.Version > encryptedVersion {
		return "", ErrNewerFormat
	}
	if err := e.check(); err != nil {
		return "", err
	}
	if passphrase == "" {
		return "", ErrLocked
	}
	aead, err := e.aead(passphrase)
	if err != nil {
		return "", err
	}
	plain, err := aead.Open(nil, e.Nonce, e.Data, e.header())
	if err != nil {
		return "", ErrWrongPassphrase
	}
	return string(plain), nil
}

// check reports ErrDamaged for an envelope this release cannot or should not open.
func (e envelope) check() error {
	switch {
	case e.Format != EncryptedFormat, e.Version < 1, e.KDF != kdfName, e.Cipher != cipherName:
		return fmt.Errorf("%w: unknown format, key derivation or cipher", ErrDamaged)
	case e.Time < 1 || e.Time > maxTime, e.Threads < 1 || e.Threads > maxThreads,
		e.Memory < 8*uint32(e.Threads) || e.Memory > maxMemory:
		return fmt.Errorf("%w: key derivation parameters out of range", ErrDamaged)
	case len(e.Salt) < saltSize, len(e.Nonce) != chacha20poly1305.NonceSizeX, len(e.Data) < chacha20poly1305.Overhead:
		return fmt.Errorf("%w: salt, nonce or data has the wrong size", ErrDamaged)
	}
	return nil
}

func (e envelope) aead(passphrase string) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), e.Salt, e.Time, e.Memory, e.Threads, keySize)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("set up the cipher: %w", err)
	}
	return aead, nil
}

// header is what the cipher authenticates besides the data: every parameter
// that is not secret, so none can be changed without the file failing to open.
func (e envelope) header() []byte {
	return []byte(strings.Join([]string{
		e.Format, strconv.Itoa(e.Version), e.KDF, strconv.FormatUint(uint64(e.Time), 10), strconv.FormatUint(uint64(e.Memory), 10),
		strconv.FormatUint(uint64(e.Threads), 10), e.Cipher,
	}, "|"))
}
