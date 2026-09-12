package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const initConfigName = "config.json"

// writeInitConfig creates or replaces the cwd-local project configuration.
func writeInitConfig(cwd string, data []byte, force bool) error {
	directory := filepath.Join(cwd, ".engram")
	if err := ensureInitConfigDirectory(directory); err != nil {
		return err
	}

	configPath := filepath.Join(directory, initConfigName)
	if err := rejectInitConfigSymlink(configPath); err != nil {
		return err
	}
	if force {
		return replaceInitConfig(configPath, data)
	}
	return createInitConfig(configPath, data)
}

func ensureInitConfigDirectory(path string) error {
	if err := os.Mkdir(path, 0o755); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create .engram directory: %w", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect .engram directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(".engram must be a real directory, not a symlink")
	}
	if !info.IsDir() {
		return fmt.Errorf(".engram must be a directory")
	}
	return nil
}

func rejectInitConfigSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect .engram/config.json: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(".engram/config.json must not be a symlink")
	}
	return nil
}

func createInitConfig(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf(".engram/config.json already exists (use --force to overwrite)")
	}
	if err != nil {
		return fmt.Errorf("write .engram/config.json: %w", err)
	}
	if err := writeAndCloseInitConfig(file, data); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("write .engram/config.json: %w (remove partial config: %v)", err, removeErr)
		}
		return fmt.Errorf("write .engram/config.json: %w", err)
	}
	return nil
}

func replaceInitConfig(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config.json-*")
	if err != nil {
		return fmt.Errorf("create replacement config: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set replacement config permissions: %w", err)
	}
	if err := writeAndCloseInitConfig(temporary, data); err != nil {
		return fmt.Errorf("write replacement config: %w", err)
	}
	if err := replaceInitConfigFile(temporaryPath, path); err != nil {
		return fmt.Errorf("publish replacement config: %w", err)
	}
	published = true
	return nil
}

func writeAndCloseInitConfig(file *os.File, data []byte) error {
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = fmt.Errorf("short write: wrote %d of %d bytes", written, len(data))
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
