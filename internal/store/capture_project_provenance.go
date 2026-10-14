package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
)

// ProjectProvenance is local capture evidence, never an ownership or enrollment grant.
// A nil value means the caller did not provide evidence.
type ProjectProvenance struct {
	Source     string `json:"source"`
	Directory  string `json:"directory,omitempty"`
	RawProject string `json:"raw_project,omitempty"`
}

var ErrInvalidProjectProvenance = errors.New("invalid project_provenance")

// UnmarshalJSON validates known field types without rejecting future properties.
func (p *ProjectProvenance) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProjectProvenance, err)
	}
	if fields == nil {
		return ErrInvalidProjectProvenance
	}
	var decoded ProjectProvenance
	for key, target := range map[string]*string{
		"source": &decoded.Source, "directory": &decoded.Directory, "raw_project": &decoded.RawProject,
	} {
		if raw, ok := fields[key]; ok {
			if string(raw) == "null" {
				return fmt.Errorf("%w: %s must be a string", ErrInvalidProjectProvenance, key)
			}
			if err := json.Unmarshal(raw, target); err != nil {
				return fmt.Errorf("%w: %s must be a string", ErrInvalidProjectProvenance, key)
			}
		}
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

func (p *ProjectProvenance) Validate() error {
	if p == nil {
		return nil
	}
	switch p.Source {
	case "", "unknown", projectpkg.SourceGitRemote, projectpkg.SourceGitRoot,
		projectpkg.SourceGitChild, projectpkg.SourceDirBasename, projectpkg.SourceAmbiguous,
		projectpkg.SourceExplicitOverride, projectpkg.SourceSessionProject,
		projectpkg.SourceUserSelectedAfterAmbiguousProject, projectpkg.SourceRequestBody,
		projectpkg.SourceConfig, projectpkg.SourceAllProjects, projectpkg.SourceProcessOverride,
		projectpkg.SourceStoredProject, projectpkg.SourceUnboundGit:
		return nil
	default:
		return fmt.Errorf("%w: unsupported source %q", ErrInvalidProjectProvenance, p.Source)
	}
}

// Called only after inserting a new canonical record, never on replay or update.
func captureProjectProvenanceTx(tx *sql.Tx, kind, id string, p *ProjectProvenance) error {
	// A deleted session ID may be registered again. Its old evidence must not
	// become evidence for the new registration, even when the new client omits it.
	if _, err := tx.Exec(`DELETE FROM capture_project_provenance WHERE entity_kind = ? AND entity_id = ?`, kind, id); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	source := p.Source
	if source == "" {
		source = "unknown"
	}
	// Existing records never reach this path, so omission and retries cannot relabel them.
	_, err := tx.Exec(`INSERT INTO capture_project_provenance
		(entity_kind, entity_id, source, directory, raw_project) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(entity_kind, entity_id) DO NOTHING`, kind, id, source, p.Directory, p.RawProject)
	return err
}

// CaptureProjectProvenance reads local evidence only. Missing metadata returns nil,
// including for records imported from Cloud or created before this API existed.
func (s *Store) CaptureProjectProvenance(kind, id string) (*ProjectProvenance, error) {
	var p ProjectProvenance
	err := s.db.QueryRow(`SELECT source, directory, raw_project FROM capture_project_provenance
		WHERE entity_kind = ? AND entity_id = ?`, kind, id).Scan(&p.Source, &p.Directory, &p.RawProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// RegisterSessionWithProjectProvenance preserves evidence in the same transaction
// as existing registration/resume checks. It changes neither ownership nor routing.
func (s *Store) RegisterSessionWithProjectProvenance(id, project, directory, mode string, resume, isolated bool, p *ProjectProvenance, timings *WriteTimings) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if isolated {
		directory = ""
		mode = SessionOwnershipProjectOwned
	}
	effective := id
	err := s.startSessionRegistration(id, project, directory, mode, resume, &effective, isolated, timings, p)
	return effective, err
}
