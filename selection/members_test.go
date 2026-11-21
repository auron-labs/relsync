package selection

import (
	"errors"
	"slices"
	"testing"
)

func members(identities ...string) []Member {
	list := make([]Member, len(identities))
	for i, id := range identities {
		list[i] = Member{Identity: id, Eligible: true}
	}
	return list
}

func TestResolveMember(t *testing.T) {
	tests := []struct {
		name    string
		members []Member
		req     MemberRequest
		want    string
		err     error
	}{
		{"a lone eligible member", []Member{{Identity: "bin/tool.exe", Eligible: true}, {Identity: "README.md"}}, MemberRequest{LogicalName: "other"}, "bin/tool.exe", nil},
		{"the recorded path wins", members("a/tool.exe", "b/tool.exe"), MemberRequest{PackagePath: "b/tool.exe", LogicalName: "tool"}, "b/tool.exe", nil},
		{"the product name wins over other members", members("tool.exe", "helper.exe"), MemberRequest{LogicalName: "tool.exe"}, "tool.exe", nil},
		{"a versioned wrapper folder may change", members("tool-2.0.0/tool.exe", "tool-2.0.0/helper.exe"), MemberRequest{PackagePath: "tool-1.0.0/tool.exe"}, "tool-2.0.0/tool.exe", nil},
		{"the leaf name finds a nested member", members("deep/er/tool.exe", "deep/helper.exe"), MemberRequest{LogicalName: "tool.exe"}, "deep/er/tool.exe", nil},
		{"two members and no name is ambiguous", members("a.exe", "b.exe"), MemberRequest{}, "", ErrAmbiguousMember},
		{"two members of the same name are ambiguous", members("x/tool.exe", "y/tool.exe"), MemberRequest{LogicalName: "tool.exe"}, "", ErrAmbiguousMember},
		{"a recorded path is not replaced by a lone member", members("other.exe"), MemberRequest{PackagePath: "tool.exe"}, "", ErrAmbiguousMember},
		{"no members", nil, MemberRequest{}, "", ErrNoEligibleMember},
		{"explicit and present", members("a.exe", "b.exe"), MemberRequest{Explicit: "b.exe"}, "b.exe", nil},
		{"explicit and absent", members("a.exe"), MemberRequest{Explicit: "b.exe"}, "", ErrInvalidMemberSelection},
		{"explicit and escaping the archive", members("a.exe"), MemberRequest{Explicit: "../a.exe"}, "", ErrInvalidMemberSelection},
		{"explicit and not runnable here", []Member{{Identity: "a.exe"}}, MemberRequest{Explicit: "a.exe"}, "", ErrIncompatibleMemberSelection},
		{"ineligible members are ignored", []Member{{Identity: "tool.dll"}, {Identity: "tool.exe", Eligible: true}}, MemberRequest{LogicalName: "tool"}, "tool.exe", nil},
	}
	for _, tt := range tests {
		got, err := ResolveMember(tt.members, tt.req)
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("%s: error = %v, want %v", tt.name, err, tt.err)
			}
			continue
		}
		if err != nil || got.Identity != tt.want {
			t.Errorf("%s: got %q, %v; want %q", tt.name, got.Identity, err, tt.want)
		}
	}
}

func TestAmbiguousMemberListsTheCompetitors(t *testing.T) {
	_, err := ResolveMember(members("b/tool.exe", "a/tool.exe"), MemberRequest{LogicalName: "tool.exe"})
	var me *MemberError
	if !errors.As(err, &me) || me.Reason != MemberAmbiguous || !slices.Equal(me.Candidates, []string{"a/tool.exe", "b/tool.exe"}) {
		t.Errorf("error = %#v", err)
	}
}

func TestResolvePersistedMember(t *testing.T) {
	got, err := ResolvePersistedMember(members("tool_2.0.1_windows_amd64/tool.exe", "tool_2.0.1_windows_amd64/README"), "tool_1.9.0_windows_amd64/tool.exe", "tool")
	if err != nil || got.Identity != "tool_2.0.1_windows_amd64/tool.exe" {
		t.Errorf("wrapper rename: %q, %v", got.Identity, err)
	}
	// Anything but the wrapper is an identity assertion: another file is never substituted.
	if _, err = ResolvePersistedMember(members("bin/other.exe"), "bin/tool.exe", "tool"); !errors.Is(err, ErrNoEligibleMember) {
		t.Errorf("different member: %v", err)
	}
	if _, err = ResolvePersistedMember(members("tool.exe"), "/etc/tool", "tool"); !errors.Is(err, ErrInvalidMemberSelection) {
		t.Errorf("invalid recorded path: %v", err)
	}
}

func TestNormalizeMemberIdentity(t *testing.T) {
	tests := map[string]string{
		"./bin/tool.exe": "bin/tool.exe", "bin\\tool.exe": "bin/tool.exe", "dir/": "dir", "a/./b.exe": "a/b.exe",
	}
	for in, want := range tests {
		if got, err := normalizeMemberIdentity(in); err != nil || got != want {
			t.Errorf("normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/abs", "\\abs", "C:\\tool.exe", "a/../b", "a//b", "con.exe", "a/tool.exe.", "a/b*.exe"} {
		if got, err := normalizeMemberIdentity(bad); err == nil {
			t.Errorf("normalize(%q) = %q, want an error", bad, got)
		}
	}
}

func TestVersionWrapperLayouts(t *testing.T) {
	tests := map[string]string{
		"tool-v1/tool.exe":                           "tool.exe",
		"gum_2.0.1_Darwin_arm64/gum":                 "gum",
		"mago-1.49.0-aarch64-apple-darwin/mago":      "mago",
		"tool/1.2.3/tool.exe":                        "tool.exe",
		"tool-windows/tool.exe":                      "tool.exe",
		"bin/tool.exe":                               "bin/tool.exe",
		"outer.zip!/tool-1.0/tool.exe":               "outer.zip!/tool.exe",
		"tool-1.0-extra/tool.exe":                    "tool-1.0-extra/tool.exe",
		"tool_1.0.0_windows_amd64_portable/tool.exe": "tool_1.0.0_windows_amd64_portable/tool.exe",
	}
	for in, want := range tests {
		if got := normalizeMemberWrapper(in); got != want {
			t.Errorf("normalizeMemberWrapper(%q) = %q, want %q", in, got, want)
		}
	}
}
