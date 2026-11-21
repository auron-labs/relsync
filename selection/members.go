package selection

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Archive-member resolution is adapted from the shared release-asset resolver
// (see NOTICE.md). The caller lists an archive's members and says which are
// eligible: executables for this machine whose bytes passed
// ValidateRunnablePayload. Resolution never depends on the order of the list.

// Errors that make an archive member unresolvable.
var (
	ErrNoEligibleMember            = errors.New("no eligible archive member")
	ErrAmbiguousMember             = errors.New("ambiguous archive member")
	ErrInvalidMemberSelection      = errors.New("invalid archive member selection")
	ErrIncompatibleMemberSelection = errors.New("incompatible archive member selection")
)

// MemberReason is a stable code for a resolution failure.
type MemberReason string

// The reasons a member can fail to resolve.
const (
	MemberNoEligible            MemberReason = "no_eligible_member"
	MemberAmbiguous             MemberReason = "ambiguous_member"
	MemberInvalidSelection      MemberReason = "invalid_selection"
	MemberIncompatibleSelection MemberReason = "incompatible_selection"
)

// MemberError says why a member could not be chosen, with the members that
// compete when the choice is ambiguous. It matches the Err sentinels.
type MemberError struct {
	Reason     MemberReason
	Member     string
	Candidates []string
}

func (e *MemberError) Error() string {
	if e.Member == "" {
		return string(e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Reason, e.Member)
}

// Unwrap lets errors.Is match the sentinel for the reason.
func (e *MemberError) Unwrap() error {
	switch e.Reason {
	case MemberNoEligible:
		return ErrNoEligibleMember
	case MemberAmbiguous:
		return ErrAmbiguousMember
	case MemberInvalidSelection:
		return ErrInvalidMemberSelection
	case MemberIncompatibleSelection:
		return ErrIncompatibleMemberSelection
	}
	return nil
}

func memberError(reason MemberReason, member string, candidates []Member) error {
	ids := make([]string, len(candidates))
	for i, m := range candidates {
		ids[i] = m.Identity
	}
	slices.Sort(ids)
	return &MemberError{Reason: reason, Member: member, Candidates: ids}
}

// Member is one file inside an archive. Identity is its path inside the
// archive, with "!/" separating archives nested in archives.
type Member struct {
	Identity string
	// Eligible says it is an executable that can run on this machine.
	Eligible bool
}

// MemberRequest says which member is wanted. Explicit is a member the user
// named; PackagePath is the member recorded by an earlier install; LogicalName
// is the product's name, which a member is expected to carry.
type MemberRequest struct {
	PackagePath string
	LogicalName string
	Explicit    string
}

// ResolveMember chooses the executable to install from an archive. A named
// member must exist and be eligible; otherwise the recorded path, then the
// product's name, then a lone eligible member decide. It reports an ambiguity
// instead of guessing.
func ResolveMember(members []Member, req MemberRequest) (Member, error) {
	if req.Explicit != "" {
		return resolveExplicitMember(members, req.Explicit)
	}
	eligible := eligibleMembers(members)
	if len(eligible) == 0 {
		return Member{}, memberError(MemberNoEligible, "", nil)
	}
	for _, step := range []struct {
		requested string
		wrapper   bool
	}{{req.PackagePath, false}, {req.LogicalName, false}, {req.PackagePath, true}, {req.LogicalName, true}} {
		if m, found, err := resolveMemberIdentity(eligible, step.requested, step.wrapper); found || err != nil {
			return m, err
		}
	}
	// A release with one eligible member is safe to install even when its
	// name does not repeat the repository's. A recorded path is an identity
	// assertion and must not be bypassed.
	if len(eligible) == 1 && req.PackagePath == "" {
		return eligible[0], nil
	}
	m, err := resolveMemberBasename(eligible, req.LogicalName)
	if errors.Is(err, ErrNoEligibleMember) && req.PackagePath == "" {
		return Member{}, memberError(MemberAmbiguous, req.LogicalName, eligible)
	}
	return m, err
}

// ResolvePersistedMember finds the member an Intent recorded in a newer
// archive. Only the versioned top-level folder some archives wrap their files
// in may differ; anything else missing is an error, never a different file.
func ResolvePersistedMember(members []Member, recorded, product string) (Member, error) {
	identity, err := normalizeMemberIdentity(recorded)
	if err != nil {
		return Member{}, memberError(MemberInvalidSelection, recorded, nil)
	}
	eligible := eligibleMembers(members)
	if m, found, err := resolveMemberIdentity(eligible, identity, false); found || err != nil {
		return m, err
	}
	identity = normalizeMemberForProduct(identity, product)
	var matches []Member
	for _, m := range eligible {
		if normalizeMemberForProduct(m.Identity, product) == identity {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return Member{}, memberError(MemberNoEligible, recorded, nil)
	case 1:
		return matches[0], nil
	}
	return Member{}, memberError(MemberAmbiguous, recorded, matches)
}

func resolveExplicitMember(members []Member, explicit string) (Member, error) {
	identity, err := normalizeMemberIdentity(explicit)
	if err != nil {
		return Member{}, memberError(MemberInvalidSelection, explicit, nil)
	}
	for _, m := range members {
		if m.Identity != identity {
			continue
		}
		if !m.Eligible {
			return Member{}, memberError(MemberIncompatibleSelection, explicit, nil)
		}
		return m, nil
	}
	return Member{}, memberError(MemberInvalidSelection, explicit, nil)
}

func eligibleMembers(members []Member) []Member {
	return slices.DeleteFunc(slices.Clone(members), func(m Member) bool { return !m.Eligible })
}

// resolveMemberIdentity finds the member whose identity equals requested,
// optionally ignoring a versioned wrapper folder on both sides.
func resolveMemberIdentity(candidates []Member, requested string, ignoreWrapper bool) (Member, bool, error) {
	if requested == "" {
		return Member{}, false, nil
	}
	want, err := normalizeMemberIdentity(requested)
	if err != nil {
		return Member{}, false, nil //nolint:nilerr // A name that is no member path matches nothing; the next step may still resolve it.
	}
	if ignoreWrapper {
		want = normalizeMemberWrapper(want)
	}
	var matches []Member
	for _, m := range candidates {
		identity := m.Identity
		if ignoreWrapper {
			identity = normalizeMemberWrapper(identity)
		}
		if identity == want {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return Member{}, false, nil
	case 1:
		return matches[0], true, nil
	}
	return Member{}, false, memberError(MemberAmbiguous, requested, matches)
}

func resolveMemberBasename(candidates []Member, requested string) (Member, error) {
	if requested == "" {
		return Member{}, memberError(MemberAmbiguous, "", candidates)
	}
	want, err := memberLeaf(requested)
	if err != nil {
		return Member{}, memberError(MemberNoEligible, "", nil)
	}
	var matches []Member
	for _, m := range candidates {
		if leaf, err := memberLeaf(m.Identity); err == nil && leaf == want {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return Member{}, memberError(MemberNoEligible, requested, nil)
	case 1:
		return matches[0], nil
	}
	return Member{}, memberError(MemberAmbiguous, requested, matches)
}

// normalizeMemberIdentity cleans a member path: it must be relative and stay
// inside the archive, "." components and a trailing separator are dropped, and
// both separators are read as one.
func normalizeMemberIdentity(name string) (string, error) {
	invalid := fmt.Errorf("invalid archive member %q", name)
	if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") || hasDrivePrefix(name) {
		return "", invalid
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, "/"), "\\")
	components := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
	if len(components) == 0 || len(components) != strings.Count(name, "/")+strings.Count(name, "\\")+1 {
		return "", invalid
	}
	cleaned := make([]string, 0, len(components))
	for _, component := range components {
		switch {
		case component == ".":
		case component == ".." || hasDrivePrefix(component) || strings.IndexByte(component, 0) >= 0:
			return "", invalid
		default:
			cleaned = append(cleaned, component)
		}
	}
	identity := strings.Join(cleaned, "/")
	if _, err := memberLeaf(identity); err != nil {
		return "", err
	}
	return identity, nil
}

func memberLeaf(identity string) (string, error) {
	leaf := identity[strings.LastIndexAny(identity, "/\\")+1:]
	if err := validatePortableName(leaf); err != nil {
		return "", fmt.Errorf("invalid archive member %q: %w", identity, err)
	}
	return leaf, nil
}

// validatePortableName reports a name that is not a single file name valid on
// every supported platform.
func validatePortableName(name string) error {
	invalid := fmt.Errorf("invalid filename %q", name)
	switch {
	case name == "" || name == "." || name == "..", strings.ContainsAny(name, "/\\"), hasDrivePrefix(name):
		return invalid
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return invalid
	case strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || strings.ContainsRune(`<>:"|?*`, r) }):
		return invalid
	case isReservedWindowsName(name):
		return invalid
	}
	return nil
}

func isReservedWindowsName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func hasDrivePrefix(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	drive := name[0]
	return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
}

// normalizeMemberWrapper drops a versioned wrapper folder named after the
// member itself, with or without its .exe.
func normalizeMemberWrapper(identity string) string {
	leaf, _ := memberLeaf(identity)
	return normalizeMemberForProduct(identity, strings.TrimSuffix(leaf, ".exe"))
}

// normalizeMemberForProduct drops a versioned top-level folder, such as
// tool-v1 or tool_2.0.1_Darwin_arm64, from a member path.
func normalizeMemberForProduct(identity, product string) string {
	if scopeEnd := strings.LastIndex(identity, "!/"); scopeEnd >= 0 {
		return identity[:scopeEnd+2] + normalizeMemberForProduct(identity[scopeEnd+2:], product)
	}
	parts := strings.Split(identity, "/")
	if len(parts) < 2 {
		return identity
	}
	leaf := parts[len(parts)-1]
	if len(parts) >= 3 && (parts[0] == leaf || parts[0] == product) && isVersionToken(parts[1]) {
		return strings.Join(parts[2:], "/")
	}
	if isVersionWrapper(parts[0], leaf) || isVersionWrapper(parts[0], product) ||
		(product != "" && strings.HasPrefix(parts[0], product) && parts[0] != product && isTargetSuffix(strings.TrimPrefix(parts[0], product))) {
		return strings.Join(parts[1:], "/")
	}
	return identity
}

// isVersionWrapper reports whether component is a versioned top-level folder
// for leaf: the leaf, a separator, a version, and only recognised target words.
func isVersionWrapper(component, leaf string) bool {
	if leaf == "" || len(component) <= len(leaf) || component[:len(leaf)] != leaf {
		return false
	}
	remainder := component[len(leaf):]
	if remainder[0] != '-' && remainder[0] != '_' {
		return false
	}
	remainder = remainder[1:]
	if isVersionToken(remainder) {
		return true
	}
	for separator := len(remainder) - 1; separator > 0; separator-- {
		if strings.ContainsRune("-_.", rune(remainder[separator])) && isVersionToken(remainder[:separator]) && isTargetSuffix(remainder[separator:]) {
			return true
		}
	}
	return false
}

// isTargetSuffix reports a suffix made only of separators and target words.
func isTargetSuffix(suffix string) bool {
	for suffix != "" {
		if !strings.ContainsRune("-_.", rune(suffix[0])) {
			return false
		}
		suffix = suffix[1:]
		matched := 0
		for end := len(suffix); end > 0; end-- {
			if end < len(suffix) && !strings.ContainsRune("-_.", rune(suffix[end])) {
				continue
			}
			if isTargetWord(strings.ToLower(suffix[:end])) {
				matched = end
				break
			}
		}
		if matched == 0 {
			return false
		}
		suffix = suffix[matched:]
	}
	return true
}

func isTargetWord(token string) bool {
	if token == "unknown" || token == "pc" || isTargetToken(token) {
		return true
	}
	for _, prefix := range []string{"linux", "win"} {
		if rest, ok := strings.CutPrefix(token, prefix); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// isVersionToken reports an optional v and dot-separated numbers.
func isVersionToken(token string) bool {
	version := strings.TrimPrefix(token, "v")
	if version == "" {
		return false
	}
	for part := range strings.SplitSeq(version, ".") {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return false
		}
	}
	return true
}

// ValidateFileName reports a name that is not a single file name valid on
// every supported system: a path, a name Windows reserves, or one with
// characters it does not allow.
func ValidateFileName(name string) error { return validatePortableName(name) }
