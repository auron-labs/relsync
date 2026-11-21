package install

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"
)

// MSICause is why a Windows Installer run failed, as far as its log says.
type MSICause string

// The causes the log is read for.
const (
	// CauseUnknown is a failure the log does not explain.
	CauseUnknown MSICause = ""
	// CauseBlocked is part of the installer blocked by application control.
	CauseBlocked MSICause = "blocked"
	// CauseAdministrator is an installer that needs an administrator account.
	CauseAdministrator MSICause = "administrator"
	// CauseOldVersion is an old version that cannot be removed.
	CauseOldVersion MSICause = "old-version"
	// CauseOtherVersion is another version of the product already installed.
	CauseOtherVersion MSICause = "other-version"
	// CauseBusy is another installation in progress.
	CauseBusy MSICause = "busy"
	// CauseFileInUse is a file the installer needs to replace being in use.
	CauseFileInUse MSICause = "file-in-use"
	// CauseDiskSpace is too little free disk space.
	CauseDiskSpace MSICause = "disk-space"
	// CauseCustomAction is a step inside the app's own installer failing.
	CauseCustomAction MSICause = "custom-action"
)

// MSIDiagnosis is what a failed run's log says went wrong.
type MSIDiagnosis struct {
	Cause MSICause
	// Message is the installer's own explanation, when the log has one. It is
	// the product's wording and is shown as it is.
	Message string
}

// causePatterns are matched against the log in order, case-insensitively. A bare
// number is not an error: a verbose log is full of them, so each pattern needs
// the words "Error" or the installer's own text.
var causePatterns = []struct {
	cause   MSICause
	pattern *regexp.Regexp
}{
	{CauseBlocked, regexp.MustCompile(`(?i)0x800711C7|Application Control policy|Smart App Control`)},
	{CauseAdministrator, regexp.MustCompile(`(?i)Error 1925|Error 1730|insufficient privileges|You must be an Administrator|requires administrator`)},
	{CauseOldVersion, regexp.MustCompile(`(?i)Error 1714|older version of .* cannot be removed`)},
	{CauseOtherVersion, regexp.MustCompile(`(?i)Error 1638|Another version of this product is already installed`)},
	{CauseBusy, regexp.MustCompile(`(?i)Error 1618|Another installation is (already )?in progress`)},
	{CauseFileInUse, regexp.MustCompile(`(?i)Error 130[3-6]|Error 1310`)},
	{CauseDiskSpace, regexp.MustCompile(`(?i)Error 1601|Error 1704|Out of disk space|disk full|not enough (free )?(disk )?space`)},
	{CauseCustomAction, regexp.MustCompile(`(?i)Error 172[0-3]`)},
}

// codeCauses are the exit codes that say the cause by themselves, so no log is
// needed.
var codeCauses = map[int]MSICause{
	1618: CauseBusy, 1638: CauseOtherVersion, 1925: CauseAdministrator, 1730: CauseAdministrator,
}

var (
	// productMessage is a line in which the installer speaks for the product.
	productMessage = regexp.MustCompile(`Product: [^\r\n]*? -- ([^\r\n]+)`)
	// genericResult is the closing line of every run, or an error number the
	// causes above explain better; neither is a message of the product's own.
	genericResult = regexp.MustCompile(`^(?:Installation (?:failed|completed)|Error \d)`)
	// licenseAsked is the setting an installer says it needs before it installs
	// quietly, as VeraCrypt's "passing ACCEPTLICENSE=YES". Only a setting the
	// installer names itself counts.
	licenseAsked = regexp.MustCompile(`(?i)passing\s+\b([A-Za-z0-9_]*(?:LICENSE|EULA)[A-Za-z0-9_]*)\s*=\s*(YES|TRUE|1)\b`)
)

// DiagnoseMSI says why an MSI run that ended with code failed, from its log.
// Some codes say it alone. Otherwise the installer's own message comes first,
// as it knows best, then the first cause the log shows. The log may be empty.
func DiagnoseMSI(code int, log string) MSIDiagnosis {
	if cause, ok := codeCauses[code]; ok {
		return MSIDiagnosis{Cause: cause}
	}
	for _, m := range productMessage.FindAllStringSubmatch(log, -1) {
		if message := strings.TrimSpace(m[1]); !genericResult.MatchString(message) {
			return MSIDiagnosis{Message: message}
		}
	}
	for _, c := range causePatterns {
		if c.pattern.MatchString(log) {
			return MSIDiagnosis{Cause: c.cause}
		}
	}
	return MSIDiagnosis{}
}

// LicenseSetting is the setting, as NAME=VALUE, that the log says the installer
// needs before it installs quietly, or "" when it asks for nothing.
func LicenseSetting(log string) string {
	m := licenseAsked.FindStringSubmatch(log)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1]) + "=" + strings.ToUpper(m[2])
}

// logTail bounds how much of a log is read; what went wrong is near its end.
const logTail = 16 << 20

// ReadMSILog reads an installer's verbose log, which may be UTF-16 with a byte
// order mark or plain text.
func ReadMSILog(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("open the installer log: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read the installer log: %w", err)
	}
	if info.Size() > logTail {
		if _, err := f.Seek(info.Size()-logTail, io.SeekStart); err != nil {
			return "", fmt.Errorf("read the installer log: %w", err)
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read the installer log: %w", err)
	}
	return decodeLog(data), nil
}

func decodeLog(data []byte) string {
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) {
		data = data[2:]
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
		}
		return string(utf16.Decode(units))
	}
	return string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
}
