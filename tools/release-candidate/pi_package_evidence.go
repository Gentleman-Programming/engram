package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type piPackageCheck struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	SourceCommit  string `json:"sourceCommit,omitempty"`
	Version       string `json:"version,omitempty"`
	TarballSHA256 string `json:"tarballSha256,omitempty"`
	Files         int    `json:"files,omitempty"`
}

type piPackageSnapshot struct {
	check                  piPackageCheck
	source, archive, input string
	sourceDigest           [32]byte
}

func preparePiPackage(ctx context.Context, repo, workspace, pi, input string) *piPackageSnapshot {
	p := &piPackageSnapshot{
		check:  piPackageCheck{Name: "pi-package-source-binding", State: "unavailable", SourceCommit: pi},
		source: filepath.Join(workspace, "pi-package-source"), archive: filepath.Join(workspace, "pi-package.tgz"), input: input,
	}
	if materializeSource(ctx, repo, pi, p.source) != nil {
		return p
	}
	var err error
	p.sourceDigest, err = sourceTreeDigest(p.source)
	if err != nil {
		return p
	}
	p.check.TarballSHA256, _ = capturePackageArchive(input, p.archive)
	return p
}

func (p *piPackageSnapshot) verify(ctx context.Context, version string) piPackageCheck {
	return p.verifyInto(ctx, version, "")
}

func (p *piPackageSnapshot) verifyInto(ctx context.Context, version, destination string) piPackageCheck {
	result := p.check
	if result.TarballSHA256 == "" || ctx.Err() != nil {
		return result
	}
	result.Version = version // Intent; source/archive validation below checks Pi.
	count, err := validatePackageInto(p.source, version, p.archive, destination)
	result.State = "not-established"
	if err == nil {
		result.State, result.Files = "passed", count
	}
	source, sourceErr := sourceTreeDigest(p.source)
	archive, archiveErr := capturePackageArchive(p.archive, "")
	input, inputErr := capturePackageArchive(p.input, "")
	if ctx.Err() != nil || sourceErr != nil || source != p.sourceDigest || archiveErr != nil || inputErr != nil || archive != result.TarballSHA256 || input != result.TarballSHA256 {
		result.State, result.Files = "unavailable", 0
	}
	return result
}

// A bounded owned copy is the input to BOTH digest evidence and the unchanged
// validator. An empty destination only rechecks bytes; it never creates a file.
func capturePackageArchive(filename, destination string) (string, error) {
	before, err := os.Lstat(filename)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxPackageBytes {
		return "", errPackageRejected
	}
	input, err := os.Open(filename)
	if err != nil {
		return "", errPackageRejected
	}
	defer func() { _ = input.Close() }()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", errPackageRejected
	}
	h := sha256.New()
	var output io.Writer = h
	var copy *os.File
	if destination != "" {
		copy, err = os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", errPackageRejected
		}
		output = io.MultiWriter(h, copy)
	}
	n, err := io.Copy(output, io.LimitReader(input, maxPackageBytes+1))
	if copy != nil {
		closeErr := copy.Close()
		if err == nil {
			err = closeErr
		}
	}
	after, statErr := input.Stat()
	current, pathErr := os.Lstat(filename)
	if err != nil || statErr != nil || pathErr != nil || n > maxPackageBytes || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, current) {
		return "", errPackageRejected
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
