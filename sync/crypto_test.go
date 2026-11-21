package sync

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const passphrase = "correct horse battery"

func TestEncryptedTextOpensWithThePassphraseAndOnlyWithIt(t *testing.T) {
	sealed, err := Encrypt("the library", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "the library") {
		t.Fatal("the plain text is in the encrypted file")
	}
	got, err := Decrypt(sealed, passphrase)
	if err != nil || got != "the library" {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}

	if _, err := Decrypt(sealed, "another passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: error = %v, want ErrWrongPassphrase", err)
	}
	if _, err := Decrypt(sealed, ""); !errors.Is(err, ErrLocked) {
		t.Errorf("no passphrase: error = %v, want ErrLocked", err)
	}
}

func TestEncryptingTheSameTextTwiceGivesDifferentFiles(t *testing.T) {
	a, _ := Encrypt("same", passphrase)
	b, _ := Encrypt("same", passphrase)
	if a == b {
		t.Error("two encryptions are identical: the salt or nonce is not fresh")
	}
}

func TestTheEnvelopeRecordsTheKeyDerivation(t *testing.T) {
	sealed, err := Encrypt("x", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	var e envelope
	if err := json.Unmarshal([]byte(sealed), &e); err != nil {
		t.Fatal(err)
	}
	if e.KDF != "argon2id" || e.Cipher != "xchacha20poly1305" || e.Time == 0 || e.Memory == 0 || e.Threads == 0 || len(e.Salt) < saltSize {
		t.Errorf("envelope = %+v, want the parameters recorded", e)
	}
}

func TestAnAlteredEncryptedFileIsRefused(t *testing.T) {
	sealed, err := Encrypt("the library", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*envelope){
		"data":    func(e *envelope) { e.Data[0] ^= 1 },
		"nonce":   func(e *envelope) { e.Nonce[0] ^= 1 },
		"salt":    func(e *envelope) { e.Salt[0] ^= 1 },
		"time":    func(e *envelope) { e.Time++ },
		"memory":  func(e *envelope) { e.Memory += 8 },
		"threads": func(e *envelope) { e.Threads++ },
	} {
		t.Run(name, func(t *testing.T) {
			var e envelope
			if err := json.Unmarshal([]byte(sealed), &e); err != nil {
				t.Fatal(err)
			}
			change(&e)
			altered, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decrypt(string(altered), passphrase)
			if err == nil {
				t.Fatalf("an altered %s still opened: %q", name, got)
			}
			if !errors.Is(err, ErrWrongPassphrase) {
				t.Errorf("error = %v, want it reported as not opening", err)
			}
		})
	}
}

func TestAMalformedEnvelopeIsDamagedNotWrongPassphrase(t *testing.T) {
	sealed, _ := Encrypt("x", passphrase)
	var good envelope
	if err := json.Unmarshal([]byte(sealed), &good); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*envelope){
		"short nonce":     func(e *envelope) { e.Nonce = e.Nonce[:3] },
		"no data":         func(e *envelope) { e.Data = nil },
		"unknown cipher":  func(e *envelope) { e.Cipher = "rot13" },
		"unknown kdf":     func(e *envelope) { e.KDF = "md5" },
		"enormous memory": func(e *envelope) { e.Memory = 1 << 30 },
		"enormous time":   func(e *envelope) { e.Time = 1000 },
		"no threads":      func(e *envelope) { e.Threads = 0 },
		"too little salt": func(e *envelope) { e.Salt = nil },
	} {
		t.Run(name, func(t *testing.T) {
			e := good
			e.Nonce, e.Data, e.Salt = append([]byte(nil), good.Nonce...), append([]byte(nil), good.Data...), append([]byte(nil), good.Salt...)
			change(&e)
			broken, _ := json.Marshal(e)
			if _, err := Decrypt(string(broken), passphrase); !errors.Is(err, ErrDamaged) {
				t.Errorf("error = %v, want ErrDamaged", err)
			}
		})
	}
	if _, err := Decrypt(`{"format": "`+EncryptedFormat+`", "data": 5}`, passphrase); !errors.Is(err, ErrDamaged) {
		t.Errorf("a wrongly typed field: error = %v, want ErrDamaged", err)
	}
}

func TestAnEnvelopeOfANewerVersionIsRefusedAsNewer(t *testing.T) {
	text := `{"format": "` + EncryptedFormat + `", "version": 9}`
	if _, err := Decrypt(text, passphrase); !errors.Is(err, ErrNewerFormat) {
		t.Errorf("error = %v, want ErrNewerFormat", err)
	}
}

func TestPlainTextIsNotMistakenForEncrypted(t *testing.T) {
	doc := Document{Format: Format, Version: Version, Items: []Item{{
		Source: "github", Owner: "o", Repo: "r", Note: `mentions "` + EncryptedFormat + `" in a note`,
	}}}
	text, err := doc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, plain := range map[string]string{
		"a document":                 text,
		"one that quotes the marker": `{"note": "` + EncryptedFormat + `"}`,
		"not JSON":                   "hello",
		"a list":                     "[]",
		"empty":                      "",
	} {
		if IsEncrypted(plain) {
			t.Errorf("%s is taken for an encrypted file", name)
		}
	}
	sealed, _ := Encrypt(text, passphrase)
	if !IsEncrypted(sealed) {
		t.Error("an encrypted file is not recognised")
	}
}

func TestPassphrasesNeedEightCharacters(t *testing.T) {
	for passphrase, ok := range map[string]bool{"": false, "1234567": false, "12345678": true, "pässwörd": true, "päss": false, "日本語のパスフレーズです": true} {
		if got := ValidatePassphrase(passphrase) == nil; got != ok {
			t.Errorf("ValidatePassphrase(%q) accepted = %v, want %v", passphrase, got, ok)
		}
	}
}
