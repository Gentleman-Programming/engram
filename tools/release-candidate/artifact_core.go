package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed artifact_core.mjs
var artifactCoreOwner []byte

type artifactCoreCheck struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	Cleanup         string `json:"cleanup"`
	Joined          bool   `json:"joined"`
	NodeVersion     string `json:"nodeVersion,omitempty"`
	ReportedVersion string `json:"reportedVersion,omitempty"`
	InstanceID      string `json:"instanceId,omitempty"`
	ExitCode        *int   `json:"exitCode,omitempty"`
	Retained        bool   `json:"retained,omitempty"`
	HelperSHA256    string `json:"helperSha256"`
}

func runArtifactCore(ctx context.Context, core []byte, digest, run string) *artifactCoreCheck {
	r := &artifactCoreCheck{Name: "production-core-lifecycle", State: "unavailable", Cleanup: "not-started", Joined: true}
	r.HelperSHA256 = fmt.Sprintf("%x", sha256.Sum256(artifactCoreOwner))
	root, err := os.MkdirTemp("", "engram-core-"+run+"-")
	if err != nil {
		return r
	}
	defer func() {
		if !r.Joined {
			r.Retained = true
			return
		}
		if os.RemoveAll(root) != nil {
			r.State, r.Cleanup, r.Retained = "unavailable", "unknown", true
		}
	}()
	// Unknown Git lookup is not proof of isolation. Never start Core inside a
	// repository whose ancestor project config could reach original user state.
	probe := exec.CommandContext(ctx, "git", "--no-replace-objects", "--no-optional-locks", "-C", root, "rev-parse", "--show-toplevel")
	out, probeErr := probe.CombinedOutput()
	if exit, ok := probeErr.(*exec.ExitError); !ok || exit.ExitCode() != 128 || !strings.Contains(string(out), "not a git repository") {
		return r
	}
	executable := filepath.Join(root, "selected-core")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	helper := filepath.Join(root, "core-owner.mjs")
	if os.WriteFile(executable, core, 0700) != nil || os.WriteFile(helper, artifactCoreOwner, 0600) != nil {
		return r
	}
	before, err := capturePackageArchive(executable, "")
	if err != nil || before != digest {
		return r
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return r
	}
	cmd := exec.CommandContext(ctx, node, helper, executable)
	cmd.Dir = root
	cmd.Env = []string{}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMP", "TEMP", "TMPDIR", "ENGRAM_DATA_DIR"} {
		directory := filepath.Join(root, key)
		if os.Mkdir(directory, 0700) != nil {
			return r
		}
		cmd.Env = append(cmd.Env, key+"="+directory)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return r
	}
	defer func() { _ = stdin.Close() }()
	var output cappedSourceOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	err = cmd.Run() // The Node owner keeps this pipe open until joined cleanup.
	if err != nil {
		if cmd.Process != nil {
			r.Joined, r.Cleanup = false, "unknown"
		}
		return r
	}
	var measured artifactCoreCheck
	if json.Unmarshal(output.buffer.Bytes(), &measured) != nil || measured.Name != r.Name {
		r.Joined, r.Cleanup = false, "unknown"
		return r
	}
	measured.HelperSHA256 = r.HelperSHA256
	*r = measured
	after, err := capturePackageArchive(executable, "")
	if err != nil || after != digest || ctx.Err() != nil || !r.Joined || r.Cleanup != "passed" {
		r.State = "unavailable"
	}
	return r
}
