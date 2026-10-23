package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type sourceIdentity struct {
	VersionIntent   string `json:"versionIntent"`
	RefIntent       string `json:"refIntent"`
	RequestedCommit string `json:"requestedCommit"`
	Commit          string `json:"commit,omitempty"`
}

type sourceCheck struct {
	Name           string `json:"name"`
	State          string `json:"state"`
	SourceCommit   string `json:"sourceCommit,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	ArtifactSHA256 string `json:"artifactSha256,omitempty"`
}

// This is an observation view, never an importable approval credential.
type sourceObservation struct {
	SchemaVersion int            `json:"schemaVersion"`
	Outcome       string         `json:"outcome"`
	Core          sourceIdentity `json:"core"`
	Pi            sourceIdentity `json:"pi"`
	Check         sourceCheck    `json:"check"`
}

func observeContract(root, coreRef, piRef, version, piVersion, core, pi string, output io.Writer) error {
	r := sourceObservation{SchemaVersion: 1, Outcome: "blocked",
		Core:  sourceIdentity{VersionIntent: version, RefIntent: coreRef, RequestedCommit: core},
		Pi:    sourceIdentity{VersionIntent: piVersion, RefIntent: piRef, RequestedCommit: pi},
		Check: sourceCheck{Name: "go-contract-freshness", State: "unavailable"}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	repo, err := sourceGit(ctx, root, "rev-parse", "--show-toplevel")
	if err == nil {
		root = strings.TrimSpace(string(repo))
		for _, identity := range []*sourceIdentity{&r.Core, &r.Pi} {
			kind, e := sourceGit(ctx, root, "cat-file", "-t", identity.RequestedCommit)
			if e != nil || strings.TrimSpace(string(kind)) != "commit" {
				err = fmt.Errorf("source unavailable")
				break
			}
			identity.Commit = identity.RequestedCommit
			// Existing local tags must agree; absent prospective tags are fine.
			if _, exists := sourceGit(ctx, root, "show-ref", "--verify", "--quiet", identity.RefIntent); exists == nil {
				ref, peelErr := sourceGit(ctx, root, "rev-parse", "--verify", identity.RefIntent+"^{commit}")
				if peelErr != nil || strings.TrimSpace(string(ref)) != identity.Commit {
					err = fmt.Errorf("source ref mismatch")
					break
				}
			} else if exit, ok := exists.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				err = fmt.Errorf("ref observation unavailable")
				break
			}
		}
	}
	if err == nil {
		workspace, e := os.MkdirTemp(root, ".release-source-evidence-")
		if e == nil {
			r.Check = runSourceCheck(ctx, root, workspace, r.Core.Commit, r.Pi.Commit, piVersion)
			// Never report success before owned workspace cleanup completes.
			if os.RemoveAll(workspace) != nil {
				r.Check.State = "unavailable"
			}
		}
	}
	if err := json.NewEncoder(output).Encode(r); err != nil {
		return err
	}
	return &assessmentExit{code: 2}
}

func sourceGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--no-optional-locks", "-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0")
	var output cappedSourceOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	err := cmd.Run()
	return output.buffer.Bytes(), err
}

// No embedded ReadFrom: io.Copy must not bypass the output limit.
type cappedSourceOutput struct{ buffer bytes.Buffer }

func (b *cappedSourceOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 64<<20 {
		return 0, fmt.Errorf("source output exceeds limit")
	}
	return b.buffer.Write(p)
}

func runSourceCheck(ctx context.Context, repo, workspace, core, pi, piVersion string) sourceCheck {
	result := sourceCheck{Name: "go-contract-freshness", State: "unavailable", SourceCommit: core}
	coreRoot, piRoot := filepath.Join(workspace, "core"), filepath.Join(workspace, "pi")
	if materializeSource(ctx, repo, core, coreRoot) != nil {
		return result
	}
	manifest, err := sourceGit(ctx, repo, "show", pi+":plugin/pi/package.json")
	if err != nil || os.MkdirAll(filepath.Join(piRoot, "plugin", "pi"), 0700) != nil || os.WriteFile(filepath.Join(piRoot, "plugin", "pi", "package.json"), manifest, 0600) != nil {
		return result
	}
	if validatePairMetadata(coreRoot, piRoot, piVersion) != nil {
		return result
	}
	artifact, err := os.ReadFile(filepath.Join(coreRoot, "docs", "contracts", "agent-contract-v1.json"))
	if err != nil {
		return result
	}
	result.ArtifactSHA256 = fmt.Sprintf("%x", sha256.Sum256(artifact))
	before, err := sourceTreeDigest(coreRoot)
	if err != nil {
		return result
	}
	// Operator-reviewed sources only. A temporary copy is NOT a sandbox, and
	// an untrusted checker can forge success or perform arbitrary side effects.
	cmd := exec.CommandContext(ctx, "go", "run", "./tools/agent-contract", "-check")
	cmd.Dir = coreRoot
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOFLAGS=-mod=readonly")
	err = cmd.Run()
	if ctx.Err() == nil && cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		result.ExitCode = &code
		result.State = "not-established"
		if err == nil {
			result.State = "passed"
		}
	}
	after, digestErr := sourceTreeDigest(coreRoot)
	if digestErr != nil || before != after {
		result.State = "unavailable"
	}
	return result
}

// Read raw Git objects, not git archive (which applies export-ignore/subst).
func materializeSource(ctx context.Context, repo, commit, root string) error {
	list, err := sourceGit(ctx, repo, "ls-tree", "-rz", "--full-tree", commit)
	if err != nil {
		return err
	}
	entries := bytes.Split(bytes.TrimSuffix(list, []byte{0}), []byte{0})
	if len(entries) > 4096 {
		return fmt.Errorf("too many source entries")
	}
	var ids []string
	var paths []string
	var modes []os.FileMode
	for _, entry := range entries {
		pair := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(pair) != 2 {
			return fmt.Errorf("invalid source tree")
		}
		meta, name := strings.Fields(string(pair[0])), string(pair[1])
		if len(meta) != 3 || meta[1] != "blob" || (meta[0] != "100644" && meta[0] != "100755") || !filepath.IsLocal(name) || strings.ContainsAny(name, "\\:") || strings.Contains("/"+name+"/", "/.git/") {
			return fmt.Errorf("unsupported source entry")
		}
		ids, paths = append(ids, meta[2]), append(paths, filepath.Join(root, filepath.FromSlash(name)))
		mode := os.FileMode(0600)
		if meta[0] == "100755" {
			mode = 0700
		}
		modes = append(modes, mode)
	}
	cmd := exec.CommandContext(ctx, "git", "--no-replace-objects", "--no-optional-locks", "-C", repo, "cat-file", "--batch")
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	var objects cappedSourceOutput
	cmd.Stdout, cmd.Stderr = &objects, io.Discard
	if err := cmd.Run(); err != nil {
		return err
	}
	reader := bufio.NewReader(&objects.buffer)
	for i, path := range paths {
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 || fields[0] != ids[i] || fields[1] != "blob" {
			return fmt.Errorf("invalid source object")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > 64<<20 {
			return fmt.Errorf("source object too large")
		}
		data := make([]byte, int(size)+1)
		if _, err := io.ReadFull(reader, data); err != nil || data[len(data)-1] != '\n' {
			return fmt.Errorf("source object truncated")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, data[:len(data)-1], modes[i]); err != nil {
			return err
		}
	}
	return nil
}

func sourceTreeDigest(root string) ([32]byte, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source changed type")
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(h, "%d:%s:%x", len(name), name, sha256.Sum256(data))
		return err
	})
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, err
}
