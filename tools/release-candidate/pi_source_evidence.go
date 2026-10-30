package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type piInputCheck struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	SourceCommit    string `json:"sourceCommit,omitempty"`
	VerifierCommit  string `json:"verifierCommit,omitempty"`
	ContractSHA256  string `json:"contractSha256,omitempty"`
	TypeBoxVersion  string `json:"typeBoxVersion,omitempty"`
	CaptureSHA256   string `json:"captureSha256,omitempty"`
	CaptureExitCode *int   `json:"captureExitCode,omitempty"`
	ExitCode        *int   `json:"exitCode,omitempty"`
}

// Same capture recipe as Go's existing real-projection tests; no schema mappings.
// Both the selected helper and plugin are operator-reviewed executable sources.
const sourcePiCaptureScript = `
import { withPluginSandbox, importPluginFromSandbox } from './plugin/pi/test/plugin-sandbox.mjs';
const capture = await withPluginSandbox('engram-pi-projection-', async ({sandbox}) => {
 const tools = []; const register = await importPluginFromSandbox(sandbox);
 register({ registerTool({name, parameters}) { tools.push({name, parameters}); }, on() {} });
 return JSON.stringify(tools);
}, {realTypeBox:true});
process.stdout.write(capture);
`

func runPiInputCheck(ctx context.Context, repo, workspace, core, pi, contract string) (result piInputCheck) {
	result = piInputCheck{Name: "pi-input-projection", State: "unavailable", SourceCommit: pi, VerifierCommit: core, ContractSHA256: contract}
	coreRoot, piRoot := filepath.Join(workspace, "core"), filepath.Join(workspace, "pi")
	if materializeSource(ctx, repo, pi, piRoot) != nil || copyPinnedTypeBox(repo, piRoot) != nil {
		return
	}
	result.TypeBoxVersion = "1.3.34"
	beforeCore, coreErr := sourceTreeDigest(coreRoot)
	beforePi, piErr := sourceTreeDigest(piRoot)
	if coreErr != nil || piErr != nil {
		return
	}
	defer func() {
		afterCore, e1 := sourceTreeDigest(coreRoot)
		afterPi, e2 := sourceTreeDigest(piRoot)
		if e1 != nil || e2 != nil || beforeCore != afterCore || beforePi != afterPi {
			result.State = "unavailable"
		}
	}()
	temporary := filepath.Join(workspace, "tmp")
	if os.Mkdir(temporary, 0700) != nil {
		return
	}
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "-e", sourcePiCaptureScript)
	cmd.Dir = piRoot
	cmd.Env = append(os.Environ(), "TMPDIR="+temporary, "TMP="+temporary, "TEMP="+temporary, "NODE_OPTIONS=", "NODE_PATH=")
	// A captured stream needs a pipe. Bound EOF waits even if a descendant keeps it.
	// This is not a process-tree killer or a sandbox.
	cmd.WaitDelay = 2 * time.Second
	var capture cappedSourceOutput
	cmd.Stdout = &capture
	err := cmd.Run()
	if ctx.Err() != nil || cmd.ProcessState == nil {
		return
	}
	captureCode := cmd.ProcessState.ExitCode()
	result.CaptureExitCode = &captureCode
	if err != nil {
		return
	}
	raw := capture.buffer.Bytes()
	result.CaptureSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	path := filepath.Join(workspace, "pi-inputs.json")
	if os.WriteFile(path, raw, 0600) != nil {
		return
	}
	cmd = exec.CommandContext(ctx, "go", "run", "./tools/agent-contract", "-verify-pi", path)
	cmd.Dir = coreRoot
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOFLAGS=-mod=readonly")
	err = cmd.Run()
	if ctx.Err() != nil || cmd.ProcessState == nil {
		return
	}
	code := cmd.ProcessState.ExitCode()
	result.ExitCode = &code
	result.State = "not-established"
	if err == nil {
		result.State = "passed"
	}
	verified, readErr := os.ReadFile(path)
	if readErr != nil || fmt.Sprintf("%x", sha256.Sum256(verified)) != result.CaptureSHA256 {
		result.State = "unavailable"
	}
	return
}

// Only the existing fixed local test dependency, never an install or path override.
// Version metadata is not package authenticity; the operator must trust this cache.
func copyPinnedTypeBox(repo, piRoot string) error {
	source := filepath.Join(repo, "plugin", "pi", "node_modules", "typebox")
	base, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(base, real)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("dependency outside repository")
	}
	destination := filepath.Join(piRoot, "plugin", "pi", "node_modules", "typebox")
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("dependency target already exists")
	}
	count, total := 0, int64(0)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported dependency entry")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		count++
		total += info.Size()
		if count > 4096 || total > 64<<20 {
			return fmt.Errorf("dependency exceeds limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(destination, "package.json"))
	if err != nil {
		return err
	}
	var manifest struct{ Name, Version string }
	if json.Unmarshal(raw, &manifest) != nil || manifest.Name != "typebox" || manifest.Version != "1.3.34" {
		return fmt.Errorf("fixed TypeBox unavailable")
	}
	return nil
}
