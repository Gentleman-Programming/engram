package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type artifactInputObservation struct {
	sourceObservation
	ArtifactInputs *artifactInputCheck `json:"artifactInputs,omitempty"`
	Workflows      *sourceCheck        `json:"workflows"`
}

type artifactInputCheck struct {
	WorkflowSHA  string `json:"workflowSha"`
	CandidateSHA string `json:"candidateSha"`
	RunID        string `json:"runId"`
	RunAttempt   string `json:"runAttempt"`
	ZIPHash      string `json:"zipSha256"`
	CoreHash     string `json:"coreSha256"`
	PiHash       string `json:"piSha256"`
	Platform     string `json:"platform"`
}

// Context is operator-reviewed input, never authentication or execution consent.
func stagingContext(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errPackageRejected
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errPackageRejected
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return nil, errPackageRejected
	}
	return b, nil
}

func observeArtifactInputs(root, coreRef, piRef, version, piVersion, core, pi, bundle, contextFile string, output io.Writer) error {
	r := artifactInputObservation{sourceObservation: sourceObservation{SchemaVersion: 1, Outcome: "blocked",
		Core:  sourceIdentity{VersionIntent: version, RefIntent: coreRef, RequestedCommit: core},
		Pi:    sourceIdentity{VersionIntent: piVersion, RefIntent: piRef, RequestedCommit: pi},
		Check: sourceCheck{Name: "staged-artifact-admission", State: "unavailable"}},
		Workflows: &sourceCheck{Name: "artifact-workflows", State: "unavailable"}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admitArtifactInputs(ctx, root, bundle, contextFile, &r)
	if err := json.NewEncoder(output).Encode(r); err != nil {
		return err
	}
	return &assessmentExit{code: 2}
}

func admitArtifactInputs(ctx context.Context, root, bundle, contextFile string, r *artifactInputObservation) {
	raw, err := stagingContext(contextFile)
	if err != nil {
		return
	}
	var want stagingExpectation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&want) != nil || decoder.Decode(new(any)) != io.EOF {
		return
	}
	fullCommit := regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	run, runErr := strconv.ParseUint(want.RunID, 10, 64)
	attempt, attemptErr := strconv.ParseUint(want.RunAttempt, 10, 64)
	if !fullCommit.MatchString(want.WorkflowSHA) || want.CandidateSHA != r.Core.RequestedCommit || runErr != nil || attemptErr != nil || run == 0 || attempt == 0 || want.CoreVersion != r.Core.VersionIntent || want.PiVersion != r.Pi.VersionIntent {
		return
	}
	repo, err := sourceGit(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return
	}
	root = strings.TrimSpace(string(repo))
	for _, identity := range []*sourceIdentity{&r.Core, &r.Pi} {
		kind, err := sourceGit(ctx, root, "cat-file", "-t", identity.RequestedCommit)
		if err != nil || strings.TrimSpace(string(kind)) != "commit" {
			return
		}
		identity.Commit = identity.RequestedCommit
	}
	measured, err := readStagedArtifacts(ctx, bundle, want)
	if err != nil {
		return
	}
	r.ArtifactInputs = &artifactInputCheck{want.WorkflowSHA, want.CandidateSHA, want.RunID, want.RunAttempt, measured.ZIPHash, measured.CoreSHA256, measured.PiSHA256, runtime.GOOS + "/" + runtime.GOARCH}
	workspace, err := os.MkdirTemp(root, ".artifact-inputs-")
	if err != nil {
		return
	}
	defer func() {
		if os.RemoveAll(workspace) != nil {
			r.Check.State = "unavailable"
			if r.PiPackage != nil {
				r.PiPackage.State, r.PiPackage.Files = "unavailable", 0
			}
		}
	}()
	input := filepath.Join(workspace, "bundle-pi.tgz")
	if os.WriteFile(input, measured.Pi, 0600) != nil {
		return
	}
	pkg := preparePiPackage(ctx, root, workspace, r.Pi.Commit, input)
	check := pkg.verifyInto(ctx, r.Pi.VersionIntent, filepath.Join(workspace, "materialized-pi"))
	r.PiPackage, r.Check.State = &check, check.State
	// Recheck original measured inputs after source binding/materialization. This
	// observes changes; neither copies nor hashes protect against hostile ABA.
	_, archiveErr := readStagedArtifacts(ctx, bundle, want)
	current, contextErr := stagingContext(contextFile)
	if archiveErr != nil || contextErr != nil || !bytes.Equal(raw, current) || ctx.Err() != nil {
		r.Check.State, r.PiPackage.State, r.PiPackage.Files = "unavailable", "unavailable", 0
	}
}
