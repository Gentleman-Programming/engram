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
	"time"
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

var linkFile = os.Link

func loadOrCreateRepositoryBinding(commonDir, legacyProject string) (repositoryBinding, error) {
	return loadOrCreateBinding(commonDir, legacyProject, true)
}

func loadOrCreateBinding(commonDir, legacyProject string, repairAbandonedClaim bool) (repositoryBinding, error) {
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
	payload := append(data, '\n')
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return repositoryBinding{}, fmt.Errorf("%w: cannot create the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return repositoryBinding{}, fmt.Errorf("%w: cannot persist the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return repositoryBinding{}, fmt.Errorf("%w: cannot persist the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
	}
	defer os.Remove(temporary)

	if err := linkFile(temporary, path); err == nil {
		return binding, nil
	} else if !errors.Is(err, fs.ErrExist) {
		// Filesystems without hard links (Android app storage rejects link(2))
		// still need an atomic, first-writer-wins publication, so claim the path
		// with an exclusive create. A directory that cannot be written fails the
		// same way as before, decided by that create rather than by the link.
		if err := publishExclusive(path, payload); err == nil {
			return binding, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return repositoryBinding{}, fmt.Errorf("%w: cannot atomically create the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
		}
	}

	// Every exit left here lost the race. The exclusive create claims the path
	// before its content lands, and link(2) reports that claim as ErrExist
	// before the publisher finishes writing, so the file can still be empty.
	// Wait for the bound, then let the result stand.
	published, err := awaitPublishedBinding(commonDir)
	if err == nil {
		return published, nil
	}
	// A claim that never received its content blocks every later publisher,
	// because the exclusive create keeps failing against it. The bound has
	// already passed, so an empty file this old is abandoned work, not a live
	// claim; drop it and publish again, once.
	if repairAbandonedClaim && discardEmptyBindingClaim(commonDir) {
		return loadOrCreateBinding(commonDir, legacyProject, false)
	}
	return repositoryBinding{}, err
}

// discardEmptyBindingClaim removes a zero-length binding that predates the wait
// this call already performed. An empty file carries no identity, so it can
// only block publication; a file with content fails closed for a human, and the
// age requirement keeps a publisher that is merely slow from losing its claim.
func discardEmptyBindingClaim(commonDir string) bool {
	path := repositoryBindingPath(commonDir)
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		return false
	}
	if time.Since(info.ModTime()) < publishedBindingReadAttempts*publishedBindingRetryDelay {
		return false
	}
	return os.Remove(path) == nil
}

const (
	publishedBindingReadAttempts = 20
	publishedBindingRetryDelay   = 5 * time.Millisecond
)

// awaitPublishedBinding waits for the writer that won the race for the path.
// After a lost race an absent or empty file is the publication window rather
// than corruption, so it retries both until the bound is spent. A binding that
// stays invalid fails closed.
func awaitPublishedBinding(commonDir string) (repositoryBinding, error) {
	var err error
	for attempt := 0; attempt < publishedBindingReadAttempts; attempt++ {
		var binding repositoryBinding
		binding, err = readRepositoryBinding(commonDir)
		if err == nil {
			return binding, nil
		}
		time.Sleep(publishedBindingRetryDelay)
	}
	return repositoryBinding{}, err
}

// publishExclusive creates path with O_EXCL so exactly one writer can claim it,
// then fills it. A failed write removes the claim instead of leaving a partial
// file that later reads would have to reject as corrupt.
func publishExclusive(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func readRepositoryBinding(commonDir string) (repositoryBinding, error) {
	data, err := os.ReadFile(repositoryBindingPath(commonDir))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return repositoryBinding{}, fmt.Errorf("%w: cannot read the binding; check Git metadata permissions or configure project_name explicitly", ErrRepositoryBinding)
		}
		return repositoryBinding{}, err
	}
	// A zero-length file carries no identity, so it means "publication in
	// progress" rather than "corrupt": the publisher claims the path with an
	// exclusive create before writing it. Treating it as absent lets the
	// caller recover instead of failing closed over an empty claim.
	if len(data) == 0 {
		return repositoryBinding{}, os.ErrNotExist
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
