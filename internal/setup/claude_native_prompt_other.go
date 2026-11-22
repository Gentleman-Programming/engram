//go:build !windows

package setup

import "errors"

// SetClaudeWindowsNativePrompt selects the Windows-only Claude prompt hook.
func SetClaudeWindowsNativePrompt(bool) error {
	return errors.New("--native-windows-prompt requires Windows")
}

func protectManagedClaudeMarketplace(string) (bool, func(), error) {
	return false, func() {}, nil
}
