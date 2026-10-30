package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
)

type stagingExpectation struct {
	WorkflowSHA, CandidateSHA, RunID, RunAttempt, CoreVersion, PiVersion, ZIPHash string
}
type stagedArtifactBytes struct {
	Core, Pi                      []byte
	CoreSHA256, PiSHA256, ZIPHash string
}

// readStagedArtifacts checks consistency with caller-pinned context, not publisher
// authentication or permission to execute. Pi content validation belongs to the
// existing package validator; this reader never extracts or executes members.
func readStagedArtifacts(ctx context.Context, filename string, want stagingExpectation) (stagedArtifactBytes, error) {
	var zero stagedArtifactBytes
	fail := func(s string) (stagedArtifactBytes, error) { return zero, fmt.Errorf("staging: %s", s) }
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if want.WorkflowSHA == "" || want.CandidateSHA == "" || want.RunID == "" || want.RunAttempt == "" || want.CoreVersion == "" || want.PiVersion == "" || want.ZIPHash == "" {
		return fail("missing pinned context")
	}
	source, err := os.Lstat(filename)
	if err != nil {
		return zero, err
	}
	if !source.Mode().IsRegular() || source.Size() > 128<<20 {
		return fail("source is not a bounded regular file")
	}
	f, err := os.Open(filename)
	if err != nil {
		return zero, err
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil {
		return zero, err
	}
	if !before.Mode().IsRegular() || !os.SameFile(source, before) || before.Size() > 128<<20 {
		return fail("source is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (128<<20)+1))
	if err != nil {
		return zero, err
	}
	after, err := f.Stat()
	if err != nil {
		return zero, err
	}
	named, err := os.Lstat(filename)
	if err != nil {
		return zero, err
	}
	if !named.Mode().IsRegular() || !os.SameFile(before, named) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(data)) != before.Size() {
		return fail("source changed or uncertain")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	if digest(data) != want.ZIPHash {
		return fail("ZIP digest mismatch")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return zero, err
	}
	expected := map[string]bool{"manifest.json": true, "npm-pack.json": true, "gentle-engram-" + want.PiVersion + ".tgz": true}
	for _, system := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			n := "engram_" + system + "_" + arch
			if system == "windows" {
				n += ".exe"
			}
			expected[n] = true
			expected[n+".buildinfo.txt"] = true
		}
	}
	members := make(map[string][]byte)
	var total uint64
	for _, entry := range z.File {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if !expected[entry.Name] || members[entry.Name] != nil || !entry.Mode().IsRegular() {
			return fail("unexpected, duplicate or nonregular member")
		}
		total += entry.UncompressedSize64
		if entry.UncompressedSize64 > 64<<20 || total > 512<<20 {
			return fail("member size limit")
		}
		r, e := entry.Open()
		if e != nil {
			return zero, e
		}
		b, e := io.ReadAll(io.LimitReader(r, (64<<20)+1))
		closeErr := r.Close()
		if e != nil {
			return zero, e
		}
		if closeErr != nil {
			return zero, closeErr
		}
		if uint64(len(b)) != entry.UncompressedSize64 {
			return fail("member size mismatch")
		}
		members[entry.Name] = b
	}
	if len(members) != len(expected) {
		return fail("incomplete inventory")
	}
	var manifest struct {
		WorkflowSHA    string `json:"workflow_sha"`
		CandidateSHA   string `json:"candidate_sha"`
		RunID          string `json:"run_id"`
		RunAttempt     string `json:"run_attempt"`
		CoreVersion    string `json:"core_version"`
		PackageName    string `json:"package_name"`
		PackageVersion string `json:"package_version"`
		Files          []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
			SRI    string `json:"sri"`
		} `json:"files"`
	}
	if err := json.Unmarshal(members["manifest.json"], &manifest); err != nil {
		return zero, err
	}
	if manifest.WorkflowSHA != want.WorkflowSHA || manifest.CandidateSHA != want.CandidateSHA || manifest.RunID != want.RunID || manifest.RunAttempt != want.RunAttempt || manifest.CoreVersion != want.CoreVersion || manifest.PackageName != "gentle-engram" || manifest.PackageVersion != want.PiVersion {
		return fail("manifest context mismatch")
	}
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		b, ok := members[file.Name]
		if !ok || file.Name == "manifest.json" || seen[file.Name] {
			return fail("manifest inventory mismatch")
		}
		seen[file.Name] = true
		h := sha512.Sum512(b)
		if file.Size != int64(len(b)) || file.SHA256 != digest(b) || file.SRI != "sha512-"+base64.StdEncoding.EncodeToString(h[:]) {
			return fail("member digest, integrity or size mismatch")
		}
	}
	if len(seen) != len(expected)-1 {
		return fail("manifest inventory incomplete")
	}
	var pack []struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Filename  string `json:"filename"`
		Integrity string `json:"integrity"`
	}
	if err := json.Unmarshal(members["npm-pack.json"], &pack); err != nil {
		return zero, err
	}
	piName := "gentle-engram-" + want.PiVersion + ".tgz"
	pi := members[piName]
	piHash := sha512.Sum512(pi)
	if len(pack) != 1 || pack[0].Name != "gentle-engram" || pack[0].Version != want.PiVersion || pack[0].Filename != piName || pack[0].Integrity != "sha512-"+base64.StdEncoding.EncodeToString(piHash[:]) {
		return fail("npm package context mismatch")
	}
	host := "engram_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		host += ".exe"
	}
	core, ok := members[host]
	if !ok {
		return fail("unsupported host")
	}
	info, err := buildinfo.Read(bytes.NewReader(core))
	if err != nil {
		return zero, err
	}
	if info.Path != "github.com/Gentleman-Programming/engram/v3/cmd/engram" || info.Main.Path != "github.com/Gentleman-Programming/engram/v3" {
		return fail("Core module identity mismatch")
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	for k, v := range map[string]string{"vcs": "git", "vcs.revision": want.CandidateSHA, "vcs.modified": "false", "GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH, "CGO_ENABLED": "0"} {
		if settings[k] != v {
			return fail("Core build metadata mismatch: " + k)
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return stagedArtifactBytes{Core: core, Pi: pi, CoreSHA256: digest(core), PiSHA256: digest(pi), ZIPHash: want.ZIPHash}, nil
}
