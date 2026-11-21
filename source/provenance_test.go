package source

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const sum = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"

// record is one attestation whose signed statement says the file was built by
// the workflow in repo.
func record(repo, workflow string) string {
	statement := fmt.Sprintf(`{"predicate":{"buildDefinition":{"externalParameters":{"workflow":{"repository":"https://github.com/%s","path":%q}}}}}`, repo, workflow)
	return fmt.Sprintf(`{"bundle":{"dsseEnvelope":{"payload":%q}}}`, base64.StdEncoding.EncodeToString([]byte(statement)))
}

func provenanceFrom(t *testing.T, status int, body string) Provenance {
	t.Helper()
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	p, err := g.Provenance(t.Context(), RepoRef{Owner: "acme", Repo: "tool"}, sum)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/repos/acme/tool/attestations/sha256:" + strings.ToLower(sum); (*seen)[0].path != want {
		t.Errorf("asked %q, want %q", (*seen)[0].path, want)
	}
	return p
}

func TestProvenanceFromTheSameRepository(t *testing.T) {
	p := provenanceFrom(t, 200, `{"attestations":[`+record("Acme/Tool", ".github/workflows/release.yml")+`]}`)
	if !p.Found || p.Repository != "Acme/Tool" || p.Workflow != ".github/workflows/release.yml" || p.BuiltElsewhere(RepoRef{Owner: "acme", Repo: "tool"}) {
		t.Fatalf("provenance = %+v", p)
	}
}

func TestProvenanceFromAnotherRepositoryIsReported(t *testing.T) {
	p := provenanceFrom(t, 200, `{"attestations":[`+record("evil/tool", "build.yml")+`]}`)
	if !p.BuiltElsewhere(RepoRef{Owner: "acme", Repo: "tool"}) || p.Repository != "evil/tool" {
		t.Fatalf("provenance = %+v", p)
	}
}

func TestProvenanceNamingTheRepositoryWinsOverAnotherRecord(t *testing.T) {
	p := provenanceFrom(t, 200, `{"attestations":[`+record("evil/tool", "a.yml")+`,`+record("acme/tool", "b.yml")+`]}`)
	if p.Repository != "acme/tool" || p.Workflow != "b.yml" {
		t.Fatalf("provenance = %+v", p)
	}
}

func TestProvenanceWithOnlyALinkStillFound(t *testing.T) {
	p := provenanceFrom(t, 200, `{"attestations":[{"bundle_url":"https://example.test/bundle"}]}`)
	if !p.Found || p.Repository != "" || p.BuiltElsewhere(RepoRef{Owner: "acme", Repo: "tool"}) {
		t.Fatalf("provenance = %+v", p)
	}
}

func TestNoProvenanceIsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"not found":    {404, `{"message":"Not Found"}`},
		"empty list":   {200, `{"attestations":[]}`},
		"no attestors": {200, `{}`},
	} {
		if p := provenanceFrom(t, tc.status, tc.body); p.Found {
			t.Errorf("%s: provenance = %+v", name, p)
		}
	}
}

func TestProvenanceFailureIsAnError(t *testing.T) {
	g, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if _, err := g.Provenance(t.Context(), RepoRef{Owner: "acme", Repo: "tool"}, sum); err == nil {
		t.Fatal("a failed lookup was taken for no record")
	}
}

func TestOwnerReadsTheAccount(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"login":"acme","name":"Acme Inc","type":"Organization","created_at":"2020-03-04T05:06:07Z"}`))
	})
	o, err := g.Owner(t.Context(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Organization || o.Name != "Acme Inc" || !o.Created.Equal(time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)) || (*seen)[0].path != "/users/acme" {
		t.Fatalf("owner = %+v asked %q", o, (*seen)[0].path)
	}
}
