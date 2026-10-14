package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
)

// This opt-in check supports the package's literal file/directory allowlist.
// It never extracts archives, executes package code, or establishes release parity.
const maxPackageBytes int64 = 64 << 20
const maxPackageFiles = 4096

var packageRejected = errors.New("Pi package tarball validation failed")

var packageRuntimeFiles = []string{
	"package.json", "README.md", "cli.js", "index.ts", "compaction-recovery.js",
	"memory-tool-chrome.js", "private-redaction.js", "mcp-template.json",
}

type packageFile struct {
	sum  [32]byte
	size int64
}

func packagePath(name string) bool {
	return name != "" && name != "." && path.Clean(name) == name && !strings.HasPrefix(name, "/") && !strings.ContainsAny(name, "\\:*?[]\x00") && name != ".." && !strings.HasPrefix(name, "../")
}

func sourcePackage(root string) (map[string]packageFile, any, error) {
	base := filepath.Join(root, "plugin", "pi")
	data, err := os.ReadFile(filepath.Join(base, "package.json"))
	if err != nil {
		return nil, nil, packageRejected
	}
	var manifest struct {
		Files []string `json:"files"`
	}
	var document any
	if json.Unmarshal(data, &manifest) != nil || json.Unmarshal(data, &document) != nil || len(manifest.Files) == 0 || len(manifest.Files) > maxPackageFiles {
		return nil, nil, packageRejected
	}
	expected := map[string]packageFile{}
	var total int64
	add := func(filename string, info os.FileInfo) error {
		if !info.Mode().IsRegular() || info.Size() > maxPackageBytes || len(expected) >= maxPackageFiles {
			return packageRejected
		}
		rel, err := filepath.Rel(base, filename)
		if err != nil {
			return packageRejected
		}
		name := filepath.ToSlash(rel)
		if !packagePath(name) {
			return packageRejected
		}
		if _, exists := expected[name]; exists {
			return nil
		}
		total += info.Size()
		if total > maxPackageBytes {
			return packageRejected
		}
		file, err := os.Open(filename)
		if err != nil {
			return packageRejected
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(file, maxPackageBytes+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return packageRejected
		}
		var sum [32]byte
		copy(sum[:], hash.Sum(nil))
		expected[name] = packageFile{sum, n}
		return nil
	}
	for _, declared := range manifest.Files {
		name := strings.TrimSuffix(declared, "/")
		if !packagePath(name) {
			return nil, nil, packageRejected
		}
		// Refuse symlinks in every component, including explicit nested file entries.
		current := base
		for _, part := range strings.Split(name, "/") {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return nil, nil, packageRejected
			}
		}
		err := filepath.WalkDir(current, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return packageRejected
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return packageRejected
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return packageRejected
			}
			return add(filename, info)
		})
		if err != nil {
			return nil, nil, packageRejected
		}
	}
	for _, required := range packageRuntimeFiles {
		if _, ok := expected[required]; !ok {
			return nil, nil, packageRejected
		}
	}
	return expected, document, nil
}

// archive/tar also reports EOF when end markers are missing. Observe each Next
// read separately from file bodies so zero-filled content cannot impersonate EOF.
type packageTarFraming struct {
	io.Reader
	bytes   int
	nonzero bool
}

func (r *packageTarFraming) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	if !r.nonzero {
		for _, b := range p[:n] {
			if b != 0 {
				r.nonzero = true
				break
			}
		}
	}
	return n, err
}

func validatePackage(root, version, filename string) (int, error) {
	expected, manifest, err := sourcePackage(root)
	if err != nil {
		return 0, packageRejected
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPackageBytes {
		return 0, packageRejected
	}
	file, err := os.Open(filename)
	if err != nil {
		return 0, packageRejected
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return 0, packageRejected
	}
	defer gz.Close()
	bounded := &io.LimitedReader{R: gz, N: maxPackageBytes + 1}
	framing := &packageTarFraming{Reader: bounded}
	reader := tar.NewReader(framing)
	seen := map[string]bool{}
	for {
		framing.bytes, framing.nonzero = 0, false
		header, err := reader.Next()
		if err == io.EOF {
			// Next may consume the final file's zero padding first, but that is
			// shorter than one block. Require both 512-byte zero end blocks.
			if framing.bytes < 1024 || framing.nonzero {
				return 0, packageRejected
			}
			break
		}
		if err != nil {
			return 0, packageRejected
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return 0, packageRejected
		}
		if !strings.HasPrefix(header.Name, "package/") {
			return 0, packageRejected
		}
		name := strings.TrimPrefix(header.Name, "package/")
		if !packagePath(name) || seen[name] {
			return 0, packageRejected
		}
		source, ok := expected[name]
		if !ok || header.Size < 0 || header.Size > maxPackageBytes {
			return 0, packageRejected
		}
		if name == "package.json" {
			// npm may normalize JSON formatting, but not the candidate's metadata.
			data, err := io.ReadAll(io.LimitReader(reader, maxPackageBytes+1))
			if err != nil || int64(len(data)) > maxPackageBytes {
				return 0, packageRejected
			}
			var packed struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}
			var document any
			if json.Unmarshal(data, &packed) != nil || packed.Name != "gentle-engram" || packed.Version != version || json.Unmarshal(data, &document) != nil || !reflect.DeepEqual(document, manifest) {
				return 0, packageRejected
			}
		} else {
			if header.Size != source.size {
				return 0, packageRejected
			}
			hash := sha256.New()
			n, err := io.Copy(hash, reader)
			if err != nil || n != source.size || !bytes.Equal(hash.Sum(nil), source.sum[:]) {
				return 0, packageRejected
			}
		}
		seen[name] = true
	}
	// Exhaust gzip to verify its trailer/CRC, rejecting nonzero data after tar EOF.
	// The decompressed-size bound also applies to PAX headers and trailing padding.
	tail := make([]byte, 32<<10)
	for {
		n, err := bounded.Read(tail)
		for _, b := range tail[:n] {
			if b != 0 {
				return 0, packageRejected
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, packageRejected
		}
	}
	if bounded.N == 0 || len(seen) != len(expected) {
		return 0, packageRejected
	}
	return len(seen), nil
}
