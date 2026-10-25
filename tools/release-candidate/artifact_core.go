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

//go:embed artifact_pi.mjs
var artifactPiOwner []byte

type artifactPiCheck struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	Cleanup      string `json:"cleanup"`
	Joined       bool   `json:"joined"`
	HelperSHA256 string `json:"helperSha256"`
	TreeSHA256   string `json:"treeSha256,omitempty"`
	ExitCode     *int   `json:"exitCode,omitempty"`
	Retained     bool   `json:"retained,omitempty"`
	mutated      bool
}

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
	return runArtifactCoreWithPi(ctx, core, digest, run, nil, nil)
}

// materialize uses only the admitted Go archive authority, before any import.
func runArtifactCoreWithPi(ctx context.Context, core []byte, digest, run string, materialize func(string) bool, pi *artifactPiCheck) *artifactCoreCheck {
	r := &artifactCoreCheck{Name: "production-core-lifecycle", State: "unavailable", Cleanup: "not-started", Joined: true}
	r.HelperSHA256 = fmt.Sprintf("%x", sha256.Sum256(artifactCoreOwner))
	root, err := os.MkdirTemp("", "engram-core-"+run+"-")
	if err != nil {
		return r
	}
	defer func() {
		if !r.Joined {
			r.Retained = true
			if pi != nil {
				pi.State, pi.Joined, pi.Cleanup, pi.Retained = "unavailable", false, "unknown", true
			}
			return
		}
		if os.RemoveAll(root) != nil {
			r.State, r.Cleanup, r.Retained = "unavailable", "unknown", true
			if pi != nil {
				pi.State, pi.Cleanup, pi.Retained = "unavailable", "unknown", true
			}
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
	piTree := filepath.Join(root, "materialized-pi")
	var treeBefore [32]byte
	if pi != nil {
		pi.HelperSHA256 = fmt.Sprintf("%x", sha256.Sum256(artifactPiOwner))
		if materialize == nil || !materialize(piTree) {
			return r
		}
		treeBefore, err = sourceTreeDigest(piTree)
		if err != nil {
			return r
		}
		pi.TreeSHA256 = fmt.Sprintf("%x", treeBefore)
		if os.WriteFile(filepath.Join(root, "pi-owner.mjs"), artifactPiOwner, 0600) != nil {
			return r
		}
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
	if pi != nil {
		cmd.Args = append(cmd.Args, filepath.Join(piTree, "index.ts"))
	}
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
	var measured struct {
		artifactCoreCheck
		PiBootstrap *artifactPiCheck `json:"piBootstrap"`
	}
	if json.Unmarshal(output.buffer.Bytes(), &measured) != nil || measured.Name != r.Name {
		r.Joined, r.Cleanup = false, "unknown"
		return r
	}
	measured.HelperSHA256 = r.HelperSHA256
	*r = measured.artifactCoreCheck
	if pi != nil {
		helperHash := pi.HelperSHA256
		if measured.PiBootstrap == nil {
			r.Joined, r.Cleanup = false, "unknown"
			pi.Joined, pi.Cleanup = false, "unknown"
		} else {
			*pi = *measured.PiBootstrap
		}
		pi.HelperSHA256, pi.TreeSHA256 = helperHash, fmt.Sprintf("%x", treeBefore)
		after, treeErr := sourceTreeDigest(piTree)
		pi.mutated = treeErr != nil || after != treeBefore
		if pi.mutated || !pi.Joined || pi.Cleanup != "passed" || ctx.Err() != nil {
			pi.State = "unavailable"
		}
		if !pi.Joined {
			r.Joined = false
		}
	}
	after, err := capturePackageArchive(executable, "")
	if err != nil || after != digest || ctx.Err() != nil || !r.Joined || r.Cleanup != "passed" {
		r.State = "unavailable"
		if pi != nil {
			pi.State = "unavailable"
		}
	}
	return r
}
