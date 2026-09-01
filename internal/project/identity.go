package project

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	repositoryBindingFilename = "engram-project-identity.json"
	repositoryBindingVersion  = 1
)

// ErrRepositoryBinding means automatic Git project detection cannot safely use
// its private repository binding. Callers must surface this rather than derive
// a potentially different name from mutable repository metadata.
var ErrRepositoryBinding = errors.New("repository identity binding unavailable")

type repositoryBinding struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Project string `json:"project"`
}

func repositoryBindingPath(commonDir string) string {
	return filepath.Join(commonDir, repositoryBindingFilename)
}

func loadOrCreateRepositoryBinding(commonDir, legacyProject string) (repositoryBinding, error) {
	if binding, err := readRepositoryBinding(commonDir); err == nil {
		return binding, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return repositoryBinding{}, err
	}

	project, err := normalizeProjectName(legacyProject)
	if err != nil || project != legacyProject {
		return repositoryBinding{}, fmt.Errorf("%w: cannot establish a canonical project label; configure project_name explicitly", ErrRepositoryBinding)
	}

	id, err := newRepositoryBindingID()
	if err != nil {
		return repositoryBinding{}, fmt.Errorf("%w: cannot create a private identifier", ErrRepositoryBinding)
	}
	binding := repositoryBinding{Version: repositoryBindingVersion, ID: id, Project: project}
	data, err := json.Marshal(binding)
	if err != nil {
		return repositoryBinding{}, fmt.Errorf("%w: cannot encode the binding", ErrRepositoryBinding)
	}

	path := repositoryBindingPath(commonDir)
	temporary := path + ".tmp-" + id
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return repositoryBinding{}, fmt.Errorf("%w: cannot create the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return repositoryBinding{}, fmt.Errorf("%w: cannot persist the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return repositoryBinding{}, fmt.Errorf("%w: cannot persist the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
	}
	defer os.Remove(temporary)

	if err := os.Link(temporary, path); err == nil {
		return binding, nil
	} else if !errors.Is(err, fs.ErrExist) {
		return repositoryBinding{}, fmt.Errorf("%w: cannot atomically create the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
	}

	return readRepositoryBinding(commonDir)
}

func readRepositoryBinding(commonDir string) (repositoryBinding, error) {
	data, err := os.ReadFile(repositoryBindingPath(commonDir))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return repositoryBinding{}, fmt.Errorf("%w: cannot read the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
		}
		return repositoryBinding{}, err
	}
	var binding repositoryBinding
	if err := json.Unmarshal(data, &binding); err != nil || !validRepositoryBinding(binding) {
		return repositoryBinding{}, fmt.Errorf("%w: binding is invalid; configure project_name explicitly before retrying", ErrRepositoryBinding)
	}
	return binding, nil
}

func validRepositoryBinding(binding repositoryBinding) bool {
	project, err := normalizeProjectName(binding.Project)
	if err != nil || project != binding.Project || binding.Version != repositoryBindingVersion {
		return false
	}
	if len(binding.ID) != 32 {
		return false
	}
	_, err = hex.DecodeString(binding.ID)
	return err == nil
}

func newRepositoryBindingID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
