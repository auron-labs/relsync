package source

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Provenance is what GitHub's build records say about one file: whether it
// holds a signed record of where the file was built.
type Provenance struct {
	// Found says GitHub holds a signed build record for the file.
	Found bool
	// Repository is the "owner/repo" the record says built the file; empty
	// when GitHub gave only a link to the record.
	Repository string
	// Workflow is the workflow file that built it, when the record says.
	Workflow string
}

// BuiltElsewhere reports a record that names a repository other than ref.
func (p Provenance) BuiltElsewhere(ref RepoRef) bool {
	return p.Found && p.Repository != "" && !strings.EqualFold(p.Repository, ref.String())
}

// attestationsDTO is GitHub's answer to a lookup of a file's records.
type attestationsDTO struct {
	Attestations []struct {
		Bundle struct {
			Envelope struct {
				Payload string `json:"payload"`
			} `json:"dsseEnvelope"`
		} `json:"bundle"`
	} `json:"attestations"`
}

// statementDTO is the signed statement of a record, as far as the build's
// workflow.
type statementDTO struct {
	Predicate struct {
		BuildDefinition struct {
			ExternalParameters struct {
				Workflow struct {
					Repository string `json:"repository"`
					Path       string `json:"path"`
				} `json:"workflow"`
			} `json:"externalParameters"`
		} `json:"buildDefinition"`
	} `json:"predicate"`
}

// Provenance asks GitHub for the signed build records of the file with the
// SHA-256 digest sum, among those of the repository. No record is the normal
// case and is a result with Found false, not an error. When a record names the
// repository itself that one is reported, otherwise the first that names any.
func (g *GitHub) Provenance(ctx context.Context, ref RepoRef, sum string) (Provenance, error) {
	var dto attestationsDTO
	path := fmt.Sprintf("%s/attestations/sha256:%s", repoPath(ref), strings.ToLower(sum))
	if err := g.getJSON(ctx, path, ref.String(), &dto); err != nil {
		var se *Error
		if errors.As(err, &se) && se.Kind == KindNotFound {
			return Provenance{}, nil
		}
		return Provenance{}, err
	}
	if len(dto.Attestations) == 0 {
		return Provenance{}, nil
	}
	// A record that GitHub gives only a link to still proves a build in this repository.
	result := Provenance{Found: true}
	for _, a := range dto.Attestations {
		record, ok := decodeStatement(a.Bundle.Envelope.Payload)
		if !ok {
			continue
		}
		if strings.EqualFold(record.Repository, ref.String()) {
			return record, nil
		}
		if result.Repository == "" {
			result = record
		}
	}
	return result, nil
}

// decodeStatement reads the workflow a record's payload names.
func decodeStatement(payload string) (Provenance, bool) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return Provenance{}, false
	}
	var statement statementDTO
	if err := json.Unmarshal(raw, &statement); err != nil {
		return Provenance{}, false
	}
	workflow := statement.Predicate.BuildDefinition.ExternalParameters.Workflow
	repo := strings.TrimSuffix(strings.Trim(strings.TrimPrefix(workflow.Repository, "https://github.com/"), "/"), ".git")
	if repo == "" {
		return Provenance{}, false
	}
	return Provenance{Found: true, Repository: repo, Workflow: workflow.Path}, true
}

// Owner is the account that owns a repository.
type Owner struct {
	Login string
	// Name is the account's display name, empty when it has none.
	Name         string
	Organization bool
	Created      time.Time
}

// Owner looks an account up by its login.
func (g *GitHub) Owner(ctx context.Context, login string) (Owner, error) {
	var dto struct {
		Login   string    `json:"login"`
		Name    string    `json:"name"`
		Type    string    `json:"type"`
		Created time.Time `json:"created_at"`
	}
	if err := g.getJSON(ctx, "/users/"+url.PathEscape(login), login, &dto); err != nil {
		return Owner{}, err
	}
	return Owner{Login: dto.Login, Name: dto.Name, Organization: strings.EqualFold(dto.Type, "Organization"), Created: dto.Created}, nil
}
