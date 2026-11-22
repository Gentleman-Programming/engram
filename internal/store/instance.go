package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/identityfile"
)

const (
	instanceIDSize    = 16
	instanceIDHexSize = instanceIDSize * 2
)

func EnsureInstanceID(dataDir string) (string, error) {
	return EnsureInstanceIDContext(context.Background(), dataDir)
}

// ReadPublishedInstanceID returns a valid identity already published in dataDir.
// An incomplete or nonstandard marker returns an empty identity. Unsupported
// volumes and read failures return an error so callers can initialize under lock.
func ReadPublishedInstanceID(dataDir string) (string, error) {
	file, err := openPublishedInstanceID(dataDir)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	// Allow the hexadecimal ID and newline, with one extra byte to detect
	// files that need the legacy reader under the initialization lock.
	const publishedSize = instanceIDHexSize + 1
	data, err := io.ReadAll(io.LimitReader(file, publishedSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > publishedSize {
		return "", nil
	}
	return parseInstanceID(data), nil
}

// EnsureInstanceIDContext reuses the shared identity authority with cancellable
// lock admission. Filesystem operations themselves are synchronous OS calls.
func EnsureInstanceIDContext(ctx context.Context, dataDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("engram: create data dir: %w", err)
	}
	unlock, err := acquireMigrationLockContext(ctx, filepath.Join(dataDir, ".instance-id.lock"))
	if err != nil {
		return "", fmt.Errorf("engram: acquire instance identity lock: %w", err)
	}
	defer unlock()
	return ensureInstanceIDLocked(ctx, dataDir)
}

// The caller holds .instance-id.lock; owned startup also holds .migrate.lock
// before publishing identity or opening SQLite. Shared callers retain their
// original lock acquisition and lifetime through EnsureInstanceID.
func ensureInstanceIDLocked(ctx context.Context, dataDir string) (string, error) {
	path := filepath.Join(dataDir, ".instance-id")
	for attempt := 0; attempt < 20; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if id, err := readInstanceID(path); err == nil {
			if id != "" {
				return id, nil
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("engram: remove incomplete instance identity: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("engram: read instance identity: %w", err)
		}
		data := make([]byte, instanceIDSize)
		if _, err := rand.Read(data); err != nil {
			return "", fmt.Errorf("engram: generate instance identity: %w", err)
		}
		id := hex.EncodeToString(data)
		file, err := os.CreateTemp(dataDir, ".instance-id-")
		if err != nil {
			return "", fmt.Errorf("engram: create temporary instance identity: %w", err)
		}
		temporaryPath := file.Name()
		if _, err := file.WriteString(id + "\n"); err != nil {
			_ = file.Close()
			_ = os.Remove(temporaryPath)
			return "", fmt.Errorf("engram: write instance identity: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = os.Remove(temporaryPath)
			return "", fmt.Errorf("engram: sync instance identity: %w", err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(temporaryPath)
			return "", fmt.Errorf("engram: close instance identity: %w", err)
		}
		err = identityfile.Publish(temporaryPath, path)
		_ = os.Remove(temporaryPath)
		if err == nil {
			return id, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("engram: publish instance identity: %w", err)
		}
		if winner, readErr := readInstanceID(path); readErr == nil {
			if winner != "" {
				return winner, nil
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("engram: read instance identity after publication race: %w", readErr)
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", fmt.Errorf("engram: read instance identity: concurrent initialization did not complete")
}

func readInstanceID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return parseInstanceID(data), nil
}

func parseInstanceID(data []byte) string {
	id := strings.TrimSpace(string(data))
	if isValidInstanceID(id) {
		return id
	}
	return ""
}

func isValidInstanceID(id string) bool {
	if len(id) != instanceIDHexSize {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
