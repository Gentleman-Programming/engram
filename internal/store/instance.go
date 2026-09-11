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
	for attempt := 0; attempt < 20; attempt++ {
		if data, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(data)); len(id) == 32 {
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
		defer os.Remove(file.Name())
		defer file.Close()
		if _, err := file.WriteString(id + "\n"); err != nil {
			return "", fmt.Errorf("engram: write instance identity: %w", err)
		}
		if err := file.Sync(); err != nil {
			return "", fmt.Errorf("engram: sync instance identity: %w", err)
		}
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("engram: close instance identity: %w", err)
		}
		err = os.Link(file.Name(), path)
		if err == nil {
			return id, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("engram: publish instance identity: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "", fmt.Errorf("engram: read instance identity: concurrent initialization did not complete")
}
