package store

import (
	"fmt"
	"path/filepath"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
)

type ProjectAliasKind string

const (
	ProjectAliasKindKey ProjectAliasKind = "key"
	ProjectAliasKindCWD ProjectAliasKind = "cwd"
)

type ProjectAlias struct {
	Kind    ProjectAliasKind `json:"kind"`
	Key     string           `json:"key"`
	Project string           `json:"project"`
}

type ProjectAliasResolution struct {
	Project  string        `json:"project"`
	KeyAlias *ProjectAlias `json:"key_alias"`
	CwdAlias *ProjectAlias `json:"cwd_alias"`
	Conflict bool          `json:"conflict"`
}

func aliasProjectName(name string) (string, error) {
	result, err := projectpkg.Resolve(projectpkg.ResolutionOptions{Mode: projectpkg.ResolutionExplicit, Explicit: name})
	return result.Project, err
}

func aliasLookupKey(kind ProjectAliasKind, key string) (string, error) {
	switch kind {
	case ProjectAliasKindKey:
		return aliasProjectName(key)
	case ProjectAliasKindCWD:
		if strings.TrimSpace(key) == "" || strings.ContainsRune(key, 0) {
			return "", fmt.Errorf("invalid project alias cwd: %q", key)
		}
		return filepath.Abs(filepath.Clean(key))
	default:
		return "", fmt.Errorf("invalid project alias kind: %q", kind)
	}
}

// SetProjectAlias records a local future-only choice, not enrollment or a
// historical ownership transfer. All validation precedes the single write.
func (s *Store) SetProjectAlias(kind ProjectAliasKind, key, project string) (*ProjectAlias, error) {
	normalized, err := aliasLookupKey(kind, key)
	if err != nil {
		return nil, err
	}
	target, err := aliasProjectName(project)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`INSERT INTO project_aliases(kind, alias_key, project) VALUES(?,?,?) ON CONFLICT(kind,alias_key) DO UPDATE SET project=excluded.project`, kind, normalized, target)
	if err != nil {
		return nil, fmt.Errorf("set project alias: %w", err)
	}
	return &ProjectAlias{Kind: kind, Key: normalized, Project: target}, nil
}

func (s *Store) ListProjectAliases() ([]ProjectAlias, error) {
	rows, err := s.db.Query(`SELECT kind,alias_key,project FROM project_aliases ORDER BY kind,alias_key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	aliases := []ProjectAlias{}
	for rows.Next() {
		var alias ProjectAlias
		if err := rows.Scan(&alias.Kind, &alias.Key, &alias.Project); err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

func (s *Store) RemoveProjectAlias(kind ProjectAliasKind, key string) (bool, error) {
	normalized, err := aliasLookupKey(kind, key)
	if err != nil {
		return false, err
	}
	result, err := s.db.Exec(`DELETE FROM project_aliases WHERE kind=? AND alias_key=?`, kind, normalized)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count != 0, err
}

func (s *Store) ResolveProjectAlias(key, cwd string) (*ProjectAliasResolution, error) {
	result := &ProjectAliasResolution{Project: key}
	normalizedKey, normalizedCWD := "", ""
	var err error
	if key != "" {
		normalizedKey, err = aliasLookupKey(ProjectAliasKindKey, key)
		if err != nil {
			return nil, err
		}
	}
	if cwd != "" {
		normalizedCWD, err = aliasLookupKey(ProjectAliasKindCWD, cwd)
		if err != nil {
			return nil, err
		}
	}
	// One query observes both entries together; disagreement never selects a winner.
	rows, err := s.db.Query(`SELECT kind,alias_key,project FROM project_aliases WHERE (kind='key' AND alias_key=? AND ?!='') OR (kind='cwd' AND alias_key=? AND ?!='')`, normalizedKey, normalizedKey, normalizedCWD, normalizedCWD)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		alias := new(ProjectAlias)
		if err := rows.Scan(&alias.Kind, &alias.Key, &alias.Project); err != nil {
			return nil, err
		}
		if alias.Kind == ProjectAliasKindKey {
			result.KeyAlias = alias
		} else {
			result.CwdAlias = alias
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result.KeyAlias != nil {
		result.Project = result.KeyAlias.Project
	}
	if result.CwdAlias != nil {
		result.Project = result.CwdAlias.Project
	}
	if result.KeyAlias != nil && result.CwdAlias != nil && result.KeyAlias.Project != result.CwdAlias.Project {
		result.Project = ""
		result.Conflict = true
	}
	return result, nil
}
