package setup

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

const managedClaudePlugin = "engram@engram"
const managedClaudeDirectory = ".engram-managed-plugin"

type claudeMarketplace struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Repo   string `json:"repo"`
	Path   string `json:"path"`
}

type claudeInstalledPlugin struct {
	ID             string `json:"id"`
	Scope          string `json:"scope"`
	Version        string `json:"version"`
	Enabled        bool   `json:"enabled"`
	InstallPath    string `json:"installPath"`
	ReadFromFolder string `json:"readFromFolder"`
}

// SetClaudeWindowsNativePrompt manages only the Claude user plugin. Claude
// owns its registration, while Engram owns the catalog steering that plugin.
func SetClaudeWindowsNativePrompt(enable bool) error {
	if enable && os.Getenv("ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE") == "0" {
		return errors.New("ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE=0 requires Bash; disable the managed native hook to select Bash")
	}
	claude, err := lookPathFn("claude")
	if err != nil {
		return fmt.Errorf("find Claude CLI: %w", err)
	}
	if enable {
		for _, dependency := range []string{"jq", "curl"} {
			if _, err := lookPathFn(dependency); err != nil {
				return fmt.Errorf("the full Claude Code plugin requires %s in PATH: %w", dependency, err)
			}
		}
	}
	root, err := managedClaudeRoot()
	if err != nil {
		return err
	}
	release, err := lockManagedClaude(root)
	if err != nil {
		return err
	}
	defer release()
	if !enable {
		if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	if err := validateManagedClaudeDrive(root); err != nil {
		return err
	}

	market, installed, err := managedClaudeInventory(claude)
	if err != nil {
		return err
	}
	owned := market.owned(root)
	if !enable && !owned {
		return nil
	}
	if !owned && market.Name != "" && (market.Source != "github" || !strings.EqualFold(market.Repo, claudeCodeMarketplace)) {
		return errors.New("the engram marketplace uses a different source; keep the existing configuration")
	}
	if !owned && market.Name == "" && installed.ID != "" {
		return errors.New("the installed Engram plugin has no recognized marketplace")
	}
	if enable {
		if err := checkClaudeUserPromptSettings(filepath.Dir(root)); err != nil {
			return err
		}
	}

	template, err := loadClaudeBundle()
	if err != nil {
		return err
	}
	if !owned && installed.ID != "" {
		active := installed.ReadFromFolder
		if active == "" {
			active = installed.InstallPath
		}
		if !filepath.IsAbs(active) {
			return errors.New("Claude returned no absolute path for the installed Engram plugin")
		}
		if err := template.verify(active, true); err != nil {
			return fmt.Errorf("installed Engram plugin differs from the bundled Bash version; update the matching core and plugin first: %w", err)
		}
	}
	bash, err := template.stage(root, "")
	if err != nil {
		return err
	}
	selected := bash
	if enable {
		pin, err := pinClaudePromptCore(root)
		if err != nil {
			return err
		}
		selected, err = template.stage(root, pin)
		if err != nil {
			return err
		}
	}

	if !owned {
		// The complete Bash catalog is ready before Claude first sees this
		// source. A restart here still has the existing Bash prompt hook.
		if err := bash.publish(root); err != nil {
			return err
		}
		if err := managedClaudeCommand(claude, "marketplace", "add", root); err != nil {
			return err
		}
	}
	market, installed, err = managedClaudeInventory(claude)
	if err != nil {
		return err
	}
	if !market.owned(root) {
		return errors.New("Claude did not register the managed Engram marketplace")
	}
	if installed.ID == "" {
		if err := managedClaudeCommand(claude, "install", managedClaudePlugin, "--scope", "user"); err != nil {
			return err
		}
		market, installed, err = managedClaudeInventory(claude)
		if err != nil {
			return err
		}
	}
	if !market.owned(root) || installed.ID != managedClaudePlugin {
		return errors.New("Claude did not retain the managed user Engram plugin")
	}
	if err := selected.publish(root); err != nil {
		return err
	}
	if err := managedClaudeCommand(claude, "marketplace", "update", "engram"); err != nil {
		return fmt.Errorf("managed prompt selection published; rerun the same setup command to reconcile Claude metadata: %w", err)
	}
	if err := managedClaudeCommand(claude, "update", managedClaudePlugin, "--scope", "user"); err != nil {
		return fmt.Errorf("managed prompt selection published; rerun the same setup command to reconcile Claude metadata: %w", err)
	}
	market, installed, err = managedClaudeInventory(claude)
	if err != nil {
		return err
	}
	if !market.owned(root) || installed.Version != selected.version ||
		!sameClaudePath(installed.ReadFromFolder, selected.folder) {
		return errors.New("managed prompt selection published; Claude did not confirm the selected user plugin folder and version")
	}
	return nil
}

func managedClaudeRoot() (string, error) {
	home, err := userHomeDir()
	if err != nil && strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) == "" {
		return "", err
	}
	profile, err := filepath.Abs(claudeCodeConfigRoot(home))
	if err != nil {
		return "", err
	}
	return filepath.Join(profile, managedClaudeDirectory), nil
}

func protectManagedClaudeMarketplace(claude string) (bool, func(), error) {
	root, err := managedClaudeRoot()
	if err != nil {
		return false, func() {}, nil // Preserve ordinary setup's existing profile handling.
	}
	release, err := lockManagedClaude(root)
	if err != nil {
		return false, nil, err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return false, release, nil
	} else if err != nil {
		release()
		return false, nil, err
	}
	market, err := readManagedClaudeMarketplace(claude, func(name string, args ...string) ([]byte, error) {
		out, err := runManagedClaudeInventoryCommand(name, args...)
		if errors.Is(err, errClaudeNestedJobUnavailable) {
			// Only this read-only ownership probe can use the legacy runner.
			// Decide which mutation path to allow from the parsed response.
			return runCommand(name, args...)
		}
		return out, err
	})
	if err != nil {
		release()
		return false, nil, err
	}
	return market.owned(root), release, nil
}

func readManagedClaudeMarketplace(claude string, run claudeSetupRunner) (claudeMarketplace, error) {
	var rows []claudeMarketplace
	if err := readManagedClaudeJSON(run, claude, &rows, "marketplace", "list", "--json"); err != nil {
		return claudeMarketplace{}, err
	}
	var market claudeMarketplace
	for _, row := range rows {
		if !strings.EqualFold(row.Name, "engram") {
			continue
		}
		if market.Name != "" || row.Name != "engram" {
			return claudeMarketplace{}, errors.New("ambiguous Claude Engram marketplace")
		}
		market = row
	}
	return market, nil
}

func managedClaudeInventory(claude string) (claudeMarketplace, claudeInstalledPlugin, error) {
	var installed claudeInstalledPlugin
	market, err := readManagedClaudeMarketplace(claude, runManagedClaudeInventoryCommand)
	if err != nil {
		return market, installed, err
	}
	var rows []claudeInstalledPlugin
	if err := readManagedClaudeJSON(runManagedClaudeInventoryCommand, claude, &rows, "list", "--json"); err != nil {
		return market, installed, err
	}
	for _, row := range rows {
		name, _, _ := strings.Cut(row.ID, "@")
		if !strings.EqualFold(name, "engram") {
			continue
		}
		if row.ID != managedClaudePlugin || row.Scope != "user" || !row.Enabled || installed.ID != "" {
			return market, installed, errors.New("managed prompt setup requires a single enabled user-scope engram@engram")
		}
		installed = row
	}
	return market, installed, nil
}

func (m claudeMarketplace) owned(root string) bool {
	return m.Source == "directory" && sameClaudePath(m.Path, root)
}

func sameClaudePath(left, right string) bool {
	if !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) {
		return true
	}
	left, leftErr := filepath.EvalSymlinks(left)
	right, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(left, right)
}

func managedClaudeCommand(claude string, args ...string) error {
	out, err := runClaudeProfileCommand(claudeLocalTimeout, claude, append([]string{"plugin"}, args...)...)
	if err != nil {
		return fmt.Errorf("claude plugin %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runManagedClaudeInventoryCommand(claude string, args ...string) ([]byte, error) {
	return runClaudeProfileCommand(claudeInventoryTimeout, claude, args...)
}

func readManagedClaudeJSON(run claudeSetupRunner, claude string, value any, args ...string) error {
	out, err := run(claude, append([]string{"plugin"}, args...)...)
	if err != nil {
		return fmt.Errorf("claude plugin %s: %w", strings.Join(args, " "), err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "[") {
		return errors.New("Claude plugin inventory did not contain an array")
	}
	if err := json.Unmarshal(out, value); err != nil {
		return fmt.Errorf("read Claude plugin inventory: %w", err)
	}
	return nil
}

func checkClaudeUserPromptSettings(profile string) error {
	raw, err := os.ReadFile(filepath.Join(profile, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings struct {
		Hooks struct {
			UserPromptSubmit []struct {
				Hooks []struct {
					Type    string   `json:"type"`
					Command string   `json:"command"`
					Args    []string `json:"args"`
				} `json:"hooks"`
			} `json:"UserPromptSubmit"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return fmt.Errorf("read Claude user settings: %w", err)
	}
	for _, group := range settings.Hooks.UserPromptSubmit {
		for _, hook := range group.Hooks {
			if hook.Type != "command" {
				continue
			}
			for _, part := range append([]string{hook.Command}, hook.Args...) {
				part = strings.ToLower(part)
				if strings.Contains(part, "user-prompt-submit.sh") ||
					strings.Contains(part, claudePromptPowerShell) ||
					strings.Contains(part, claudeNativePromptHook) {
					return errors.New("an Engram prompt hook is already configured directly in Claude user settings")
				}
			}
		}
	}
	return nil
}

func validateManagedClaudeDrive(root string) error {
	drive, err := windows.UTF16PtrFromString(filepath.VolumeName(root) + "\\")
	if err != nil || windows.GetDriveType(drive) != windows.DRIVE_FIXED {
		return errors.New("managed Claude prompt setup requires a local Windows drive")
	}
	fsName := make([]uint16, 32)
	if err := windows.GetVolumeInformation(drive, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err != nil {
		return fmt.Errorf("inspect the Claude profile filesystem: %w", err)
	}
	if windows.UTF16ToString(fsName) != "NTFS" {
		return errors.New("managed Claude prompt setup requires an NTFS profile")
	}
	return rejectClaudeReparsePoint(root)
}

func lockManagedClaude(root string) (func(), error) {
	// Both setup routes take this lock, including before the first managed
	// directory exists. A mutex abandoned by a killed setup is recoverable.
	digest := sha256.Sum256([]byte(strings.ToUpper(claudeSetupLockPath(root))))
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Global\EngramClaudeSetup-%x`, digest))
	if err != nil {
		return nil, err
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	runtime.LockOSThread() // Only the acquiring Windows thread can release a mutex.
	state, err := windows.WaitForSingleObject(mutex, 0)
	if err != nil || (state != windows.WAIT_OBJECT_0 && state != windows.WAIT_ABANDONED) {
		windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		if err == nil {
			err = errors.New("another setup is using this Claude profile")
		}
		return nil, err
	}
	return func() {
		windows.ReleaseMutex(mutex)
		windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
	}, nil
}

func claudeSetupLockPath(path string) string {
	// Resolve aliases in existing ancestors before the managed directory
	// exists. This gives a DOS short name and the long path the same lock.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(claudeSetupLockPath(parent), filepath.Base(path))
}

func ensureClaudeManagedDirectory(dir string) error {
	if err := rejectClaudeReparsePoint(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0700)
}

func rejectClaudeReparsePoint(path string) error {
	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(name)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("managed Claude path contains a reparse point: %s", current)
		}
	}
	return nil
}
