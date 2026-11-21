package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Gentleman-Programming/engram/v3/internal/identityfile"
)

// HasImplicitGitProject reports whether the selection has a private Git binding.
func (r DetectionResult) HasImplicitGitProject() bool {
	return r.repositoryID != ""
}

// ImplicitGitProjectCollisionWarning shares the local advisory across capture clients.
func (r DetectionResult) ImplicitGitProjectCollisionWarning(dataDir string) string {
	if !r.FirstImplicitGitProjectCollision(dataDir) {
		return ""
	}
	return fmt.Sprintf("Warning: project %q is also selected automatically in another Git checkout. Run engram init <distinct-name> to separate future captures.", r.Project)
}

// FirstImplicitGitProjectCollision records a local implicit capture attempt and
// reports the first conflicting use by this repository. The advisory neither
// reserves a project name nor changes the resolved owner of a capture.
func (r DetectionResult) FirstImplicitGitProjectCollision(dataDir string) bool {
	if !r.HasImplicitGitProject() || r.Project == "" || dataDir == "" {
		return false
	}
	key := sha256.Sum256([]byte(r.Project))
	dir := filepath.Join(dataDir, "git-project-advisories-v1", hex.EncodeToString(key[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}

	ownerPath := filepath.Join(dir, "first-checkout")
	owner, err := identityfile.ReadFile(ownerPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = publishCaptureCheckout(ownerPath, r.repositoryID)
		if !errors.Is(err, fs.ErrExist) {
			return false
		}
		owner, err = identityfile.ReadFile(ownerPath)
	}
	if err != nil || string(owner) == r.repositoryID {
		return false
	}
	id, err := hex.DecodeString(string(owner))
	if err != nil || len(id) != 16 {
		return false
	}
	return os.Mkdir(filepath.Join(dir, "warned-"+r.repositoryID), 0o700) == nil
}

func publishCaptureCheckout(path, repositoryID string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".checkout-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	_, writeErr := file.WriteString(repositoryID)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return identityfile.Publish(file.Name(), path)
}
