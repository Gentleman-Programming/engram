package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func EnsureInstanceID(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("engram: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, ".instance-id")
	unlock, err := acquireMigrationLock(filepath.Join(dataDir, ".instance-id.lock"))
	if err != nil {
		return "", fmt.Errorf("engram: acquire instance identity lock: %w", err)
	}
	defer unlock()

	for attempt := 0; attempt < 20; attempt++ {
		if data, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(data)); isValidInstanceID(id) {
				return id, nil
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("engram: remove incomplete instance identity: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("engram: read instance identity: %w", err)
		}
		data := make([]byte, 16)
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
		err = os.Link(temporaryPath, path)
		_ = os.Remove(temporaryPath)
		if err == nil {
			return id, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("engram: publish instance identity: %w", err)
		}
		if data, readErr := os.ReadFile(path); readErr == nil {
			if winner := strings.TrimSpace(string(data)); isValidInstanceID(winner) {
				return winner, nil
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("engram: read instance identity after publication race: %w", readErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "", fmt.Errorf("engram: read instance identity: concurrent initialization did not complete")
}

func isValidInstanceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
