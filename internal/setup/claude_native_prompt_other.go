//go:build !windows

package setup

import (
	"errors"
	"time"
)

func claudeOrdinarySetupRunner(bool) claudeSetupRunner {
	return runCommand
}

func runClaudeProfileProcess(_ time.Duration, name string, args ...string) ([]byte, error) {
	return runCommand(name, args...)
}

// SetClaudeWindowsNativePrompt selects the Windows-only Claude prompt hook.
func SetClaudeWindowsNativePrompt(bool) error {
	return errors.New("--native-windows-prompt requires Windows")
}

func protectManagedClaudeMarketplace(string) (bool, func(), error) {
	return false, func() {}, nil
}
