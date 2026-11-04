package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

type publishedPairContext struct {
	Core publishedCoreContext `json:"core"`
	Pi   struct {
		Version       string `json:"version"`
		Source        string `json:"source"`
		TarballSHA256 string `json:"tarball_sha256"`
	} `json:"pi"`
}

type publishedPairObservation struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Scope         string               `json:"scope"`
	Outcome       string               `json:"outcome"`
	Admission     string               `json:"admission"`
	Identity      publishedPairContext `json:"identity"`
	Package       *piPackageCheck      `json:"package,omitempty"`
	Runtime       *artifactCoreCheck   `json:"runtime,omitempty"`
	PiRuntime     *artifactPiCheck     `json:"piRuntime,omitempty"`
}

func runPublishedPair(args []string, output io.Writer) error {
	f := flag.NewFlagSet("observe-published-pair", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	archive := f.String("archive", "", "selected published Core ZIP")
	sums := f.String("checksums", "", "selected checksums.txt")
	pinsFile := f.String("context", "", "operator-reviewed Core/Pi pins")
	tarball := f.String("pi-tarball", "", "selected published Pi tarball")
	repo := f.String("root", ".", "local Git repository containing reviewed Pi source")
	execute := f.Bool("execute", false, "execute admitted pair in private state")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *archive == "" || *sums == "" || *pinsFile == "" || *tarball == "" || *repo == "" {
		return fmt.Errorf("published pair requires archive, checksums, context, pi-tarball and root")
	}
	r := publishedPairObservation{SchemaVersion: 1, Scope: "published-pair-windows-amd64", Outcome: "blocked", Admission: "unavailable"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	raw, err := stagingContext(*pinsFile)
	if err == nil {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		err = d.Decode(&r.Identity)
		if err == nil && d.Decode(new(any)) != io.EOF {
			err = errPackageRejected
		}
	}
	if err == nil && runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		observePublishedPair(ctx, *repo, *archive, *sums, *tarball, *pinsFile, raw, *execute, &r)
	}
	if err := json.NewEncoder(output).Encode(r); err != nil {
		return err
	}
	return &assessmentExit{code: 2}
}

func observePublishedPair(ctx context.Context, repo, archive, sums, tarball, pinsFile string, raw []byte, execute bool, r *publishedPairObservation) {
	pi := r.Identity.Pi
	if _, ok := refVersion("refs/tags/pi-v"+pi.Version, "refs/tags/pi-v"); !ok {
		return
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pi.Source) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(pi.TarballSHA256) {
		return
	}
	core, err := admitPublishedCore(ctx, archive, sums, r.Identity.Core)
	if err != nil {
		return
	}
	if _, err = readPinnedArtifact(tarball, pi.TarballSHA256, 64<<20); err != nil {
		return
	}
	workspace, err := os.MkdirTemp("", "engram-published-pair-")
	if err != nil {
		return
	}
	defer func() {
		if os.RemoveAll(workspace) != nil {
			invalidatePublishedPair(r)
		}
	}()
	pkg := preparePiPackage(ctx, repo, workspace, pi.Source, tarball)
	check := pkg.verify(ctx, pi.Version)
	r.Package = &check
	if check.State != "passed" || check.TarballSHA256 != pi.TarballSHA256 {
		return
	}
	recheck := func() bool {
		if ctx.Err() != nil {
			return false
		}
		if _, e := readPinnedArtifact(archive, r.Identity.Core.ArchiveSHA256, 128<<20); e != nil {
			return false
		}
		if _, e := readPinnedArtifact(sums, r.Identity.Core.ChecksumsSHA256, 1<<20); e != nil {
			return false
		}
		if _, e := readPinnedArtifact(tarball, pi.TarballSHA256, 64<<20); e != nil {
			return false
		}
		after, e := stagingContext(pinsFile)
		return e == nil && bytes.Equal(raw, after) && pkg.verify(ctx, pi.Version).State == "passed"
	}
	if !recheck() {
		return
	}
	r.Admission = "passed"
	if execute {
		peer := filepath.Join(repo, "plugin", "pi", "node_modules", "typebox")
		before, e := sourceTreeDigest(peer)
		if e != nil {
			r.Admission = "unavailable"
			return
		}
		materialize := func(dest string) bool {
			now, e := sourceTreeDigest(peer)
			return e == nil && now == before && recheck() && pkg.verifyInto(ctx, pi.Version, dest).State == "passed" && copyPinnedTypeBox(repo, filepath.Dir(dest)) == nil
		}
		r.PiRuntime = &artifactPiCheck{Name: "published-pi-persistence", State: "unavailable", Cleanup: "not-started", Joined: true}
		r.Runtime = runArtifactObservation(ctx, core, fmt.Sprintf("%x", sha256.Sum256(core)), "published-pair", materialize, r.PiRuntime, "", r.Identity.Core.Version)
		if r.PiRuntime.mutated {
			invalidatePublishedPair(r)
		}
		after, e := sourceTreeDigest(peer)
		if e != nil || after != before {
			invalidatePublishedPair(r)
		}
	}
	if !recheck() {
		invalidatePublishedPair(r)
	}
}

func invalidatePublishedPair(r *publishedPairObservation) {
	r.Admission = "unavailable"
	if r.Package != nil {
		r.Package.State = "unavailable"
	}
	if r.Runtime != nil {
		r.Runtime.State = "unavailable"
		if r.Runtime.Persistence != "" {
			r.Runtime.Persistence = "unavailable"
		}
	}
	if r.PiRuntime != nil {
		r.PiRuntime.State = "unavailable"
		for i := range r.PiRuntime.Stages {
			r.PiRuntime.Stages[i].State = "unavailable"
		}
	}
}
