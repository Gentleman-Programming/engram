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
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Pins are operator-reviewed consistency inputs, not publisher authentication.
type publishedCoreContext struct {
	Version         string `json:"version"`
	Source          string `json:"source"`
	ArchiveSHA256   string `json:"archive_sha256"`
	ChecksumsSHA256 string `json:"checksums_sha256"`
}

func readPinnedArtifact(name, digest string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errPackageRejected
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, errPackageRejected
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errPackageRejected
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	after, e := f.Stat()
	named, n := os.Lstat(name)
	if err != nil || e != nil || n != nil || !os.SameFile(info, named) ||
		len(b) > int(limit) || int64(len(b)) != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) ||
		fmt.Sprintf("%x", sha256.Sum256(b)) != digest {
		return nil, errPackageRejected
	}
	return b, nil
}

func admitPublishedCore(ctx context.Context, archive, checksums string, pins publishedCoreContext) ([]byte, error) {
	if _, ok := refVersion("refs/tags/v"+pins.Version, "refs/tags/v"); !ok {
		return nil, errPackageRejected
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pins.Source) || !hex64.MatchString(pins.ArchiveSHA256) || !hex64.MatchString(pins.ChecksumsSHA256) {
		return nil, errPackageRejected
	}
	sums, err := readPinnedArtifact(checksums, pins.ChecksumsSHA256, 1<<20)
	if err != nil {
		return nil, err
	}
	asset := "engram_" + pins.Version + "_windows_amd64.zip"
	matches := 0
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			if fields[0] != pins.ArchiveSHA256 {
				return nil, errPackageRejected
			}
			matches++
		}
	}
	if matches != 1 {
		return nil, errPackageRejected
	}
	data, err := readPinnedArtifact(archive, pins.ArchiveSHA256, 128<<20)
	if err != nil {
		return nil, err
	}
	// Final GoReleaser layout differs from staging; both use one member validator.
	files, err := readArtifactMembers(ctx, data, map[string]bool{
		"engram.exe":                   true,
		"tools/cloud-sync-projects.sh": true, "tools/cloud-sync-projects.ps1": true,
	})
	if err != nil {
		return nil, err
	}
	core := files["engram.exe"]
	if err := validateArtifactCoreBuild(core, pins.Source); err != nil {
		return nil, err
	}
	return core, nil
}

func runPublishedCore(args []string, output io.Writer) error {
	f := flag.NewFlagSet("observe-published-core", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	archive := f.String("archive", "", "selected final ZIP")
	sums := f.String("checksums", "", "selected checksums.txt")
	pinsFile := f.String("context", "", "reviewed identity pins")
	execute := f.Bool("execute", false, "execute admitted Core in owned state")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *archive == "" || *sums == "" || *pinsFile == "" {
		return fmt.Errorf("published Core requires archive, checksums and context")
	}
	result := struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Scope         string               `json:"scope"`
		Outcome       string               `json:"outcome"`
		Admission     string               `json:"admission"`
		Identity      publishedCoreContext `json:"identity"`
		CoreSHA256    string               `json:"coreSha256,omitempty"`
		Runtime       *artifactCoreCheck   `json:"runtime,omitempty"`
	}{SchemaVersion: 1, Scope: "published-core-windows-amd64", Outcome: "blocked", Admission: "unavailable"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	data, err := stagingContext(*pinsFile)
	if err == nil {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		err = d.Decode(&result.Identity)
		if err == nil {
			var extra any
			if d.Decode(&extra) != io.EOF {
				err = errPackageRejected
			}
		}
	}
	if err == nil && runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		core, err := admitPublishedCore(ctx, *archive, *sums, result.Identity)
		if err == nil {
			result.Admission = "passed"
			result.CoreSHA256 = fmt.Sprintf("%x", sha256.Sum256(core))
			if *execute {
				result.Runtime = runArtifactCoreObservation(ctx, core, result.CoreSHA256, "published", result.Identity.Version)
			}
			// Recheck originals even for admission-only observations.
			if _, err := readPinnedArtifact(*archive, result.Identity.ArchiveSHA256, 128<<20); err != nil {
				result.Admission = "unavailable"
			}
			if _, err := readPinnedArtifact(*sums, result.Identity.ChecksumsSHA256, 1<<20); err != nil {
				result.Admission = "unavailable"
			}
			if after, err := stagingContext(*pinsFile); err != nil || !bytes.Equal(after, data) {
				result.Admission = "unavailable"
			}
			if result.Admission != "passed" && result.Runtime != nil {
				result.Runtime.State = "unavailable"
			}
		}
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return err
	}
	return &assessmentExit{code: 2} // Observations never authorize a release.
}
