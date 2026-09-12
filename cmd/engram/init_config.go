package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const initConfigName = "config.json"

// initConfigAfterOpen is a test hook for exercising the stable-directory
// boundary after .engram has been opened and validated.
var initConfigAfterOpen = func() {}

// initConfigRename is injectable only to verify failed forced publication.
// Production publication remains descriptor- or handle-bound in the platform
// implementation; this seam runs immediately before that operation.
var initConfigRename = func(_, _ string) error { return nil }

type stableInitConfigDirectory interface {
	close() error
	inspect(name string) (exists bool, symlink bool, err error)
	create(name string, perm os.FileMode) (*os.File, error)
	createTemp(pattern string, perm os.FileMode) (*os.File, string, error)
	remove(name string) error
	rename(oldName, newName string) error
}

// writeInitConfig publishes config data through a stable .engram directory.
// Non-force creation is exclusive; forced replacement writes a complete sibling
// file before atomically publishing it over the old config.
func writeInitConfig(cwd string, data []byte, force bool) error {
	directory, err := openStableInitConfigDirectory(filepath.Join(cwd, ".engram"))
	if err != nil {
		return err
	}
	defer directory.close()

	initConfigAfterOpen()

	exists, symlink, err := directory.inspect(initConfigName)
	if err != nil {
		return fmt.Errorf("inspect .engram/config.json: %w", err)
	}
	if symlink {
		return fmt.Errorf(".engram/config.json must not be a symlink")
	}
	if exists && !force {
		return fmt.Errorf(".engram/config.json already exists (use --force to overwrite)")
	}

	if !force {
		return createInitConfigExclusively(directory, data)
	}
	return replaceInitConfigAtomically(directory, data)
}

func createInitConfigExclusively(directory stableInitConfigDirectory, data []byte) error {
	file, err := directory.create(initConfigName, 0o644)
	if errors.Is(err, os.ErrExist) {
		exists, symlink, inspectErr := directory.inspect(initConfigName)
		if inspectErr != nil {
			return fmt.Errorf("inspect .engram/config.json: %w", inspectErr)
		}
		if exists && symlink {
			return fmt.Errorf(".engram/config.json must not be a symlink")
		}
		return fmt.Errorf(".engram/config.json already exists (use --force to overwrite)")
	}
	if err != nil {
		return fmt.Errorf("write .engram/config.json: %w", err)
	}
	if err := writeAndCloseInitConfig(file, data); err != nil {
		if removeErr := directory.remove(initConfigName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("write .engram/config.json: %w (remove partial config: %v)", err, removeErr)
		}
		return fmt.Errorf("write .engram/config.json: %w", err)
	}
	return nil
}

func replaceInitConfigAtomically(directory stableInitConfigDirectory, data []byte) error {
	temporary, temporaryName, err := directory.createTemp(".config.json-*", 0o644)
	if err != nil {
		return fmt.Errorf("create replacement config: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = directory.remove(temporaryName)
		}
	}()

	if err := writeAndCloseInitConfig(temporary, data); err != nil {
		return fmt.Errorf("write replacement config: %w", err)
	}
	if err := initConfigRename(temporaryName, initConfigName); err != nil {
		return fmt.Errorf("publish replacement config: %w", err)
	}
	if err := directory.rename(temporaryName, initConfigName); err != nil {
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
