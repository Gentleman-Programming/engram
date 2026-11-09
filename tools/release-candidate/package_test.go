package main_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type packedEntry struct {
	name, body string
	kind       byte
}

func packageFixture(t *testing.T) (string, []packedEntry) {
	t.Helper()
	root := candidateFixture(t, "1.2.3")
	files := []string{"cli.js", "index.ts", "compaction-recovery.js", "memory-tool-chrome.js", "private-redaction.js", "mcp-template.json", "README.md", "assets/", "test/", "package.json"}
	manifest, err := json.Marshal(map[string]any{"name": "gentle-engram", "version": "1.2.3", "files": files})
	if err != nil {
		t.Fatal(err)
	}
	candidateWrite(t, root, "plugin/pi/package.json", string(manifest))
	entries := []packedEntry{{"package/package.json", string(manifest), tar.TypeReg}}
	for _, name := range []string{"cli.js", "index.ts", "compaction-recovery.js", "memory-tool-chrome.js", "private-redaction.js", "mcp-template.json", "README.md", "assets/logo.png", "test/check.test.mjs"} {
		body := "source bytes for " + name + "\n"
		candidateWrite(t, root, "plugin/pi/"+name, body)
		entries = append(entries, packedEntry{"package/" + name, body, tar.TypeReg})
	}
	return root, entries
}

func packageArchive(t *testing.T, entries []packedEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0644, Size: int64(len(entry.body)), Typeflag: entry.kind}
		if entry.kind == tar.TypeSymlink || entry.kind == tar.TypeLink {
			header.Linkname = "index.ts"
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestPiPackageTarballCLI(t *testing.T) {
	cases := []string{"valid", "independent versions", "matching large manifest integer", "changed large manifest integer", "legacy regular file type", "empty tarball argument", "wrong name", "wrong version", "changed manifest", "missing asset", "changed asset", "extra file", "duplicate", "traversal", "absolute", "symlink", "hardlink", "malformed gzip", "truncated gzip", "truncated tar", "missing tar end markers", "one tar end block", "zero-filled file without tar end markers", "gzip checksum", "trailing data", "source changed", "missing tarball", "unsupported allowlist", "missing declared source", "missing runtime declaration"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			root, entries := packageFixture(t)
			switch name {
			case "matching large manifest integer", "changed large manifest integer":
				entries[0].body = strings.TrimSuffix(entries[0].body, "}") + `,"numericMetadata":9007199254740992}`
				candidateWrite(t, root, "plugin/pi/package.json", entries[0].body)
				if name == "changed large manifest integer" {
					entries[0].body = strings.Replace(entries[0].body, "9007199254740992", "9007199254740993", 1)
				}
			case "wrong name":
				entries[0].body = `{"name":"other","version":"1.2.3"}`
			case "wrong version":
				entries[0].body = `{"name":"gentle-engram","version":"1.2.4"}`
			case "changed manifest":
				entries[0].body = `{"name":"gentle-engram","version":"1.2.3","files":[]}`
			case "missing asset", "changed asset":
				for i, entry := range entries {
					if entry.name == "package/assets/logo.png" {
						if name == "missing asset" {
							entries = append(entries[:i], entries[i+1:]...)
						} else {
							entries[i].body = "changed"
						}
						break
					}
				}
			case "zero-filled file without tar end markers":
				entries[len(entries)-1].body = string(make([]byte, 1024))
				candidateWrite(t, root, "plugin/pi/test/check.test.mjs", entries[len(entries)-1].body)
			case "extra file":
				entries = append(entries, packedEntry{"package/unexpected.js", "extra", tar.TypeReg})
			case "duplicate":
				entries = append(entries, entries[1])
			case "traversal":
				entries[1].name = "package/../outside"
			case "absolute":
				entries[1].name = "/package/cli.js"
			case "symlink":
				entries[1].kind = tar.TypeSymlink
			case "hardlink":
				entries[1].kind = tar.TypeLink
			case "source changed":
				candidateWrite(t, root, "plugin/pi/index.ts", "changed after packing")
			case "unsupported allowlist":
				candidateWrite(t, root, "plugin/pi/package.json", `{"name":"gentle-engram","version":"1.2.3","files":["**"]}`)
			case "missing declared source":
				if err := os.Remove(filepath.Join(root, "plugin/pi/index.ts")); err != nil {
					t.Fatal(err)
				}
			case "missing runtime declaration":
				candidateWrite(t, root, "plugin/pi/package.json", `{"name":"gentle-engram","version":"1.2.3","files":["package.json","README.md"]}`)
			}
			archive := packageArchive(t, entries)
			switch name {
			case "malformed gzip":
				archive = []byte("not gzip")
			case "truncated gzip":
				archive = archive[:len(archive)/2]
			case "truncated tar":
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				_, _ = gz.Write([]byte("short tar"))
				_ = gz.Close()
				archive = b.Bytes()
			case "legacy regular file type", "missing tar end markers", "one tar end block", "zero-filled file without tar end markers":
				gz, err := gzip.NewReader(bytes.NewReader(archive))
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(gz)
				if err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				if name == "legacy regular file type" {
					// Exercise the actual legacy NUL flag, not Writer's normalized output.
					data[156] = 0
					copy(data[148:156], "        ")
					checksum := 0
					for _, b := range data[:512] {
						checksum += int(b)
					}
					copy(data[148:156], fmt.Sprintf("%06o\x00 ", checksum))
				} else {
					missing := 1024
					if name == "one tar end block" {
						missing = 512
					}
					data = data[:len(data)-missing]
				}
				var b bytes.Buffer
				writer := gzip.NewWriter(&b)
				if _, err := writer.Write(data); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				archive = b.Bytes()
			case "gzip checksum":
				archive[len(archive)-8] ^= 1
			case "trailing data":
				archive = append(archive, []byte("unexpected trailing bytes")...)
			}
			tarball := filepath.Join(root, "candidate.tgz")
			if name != "missing tarball" {
				if err := os.WriteFile(tarball, archive, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if name == "empty tarball argument" {
				tarball = ""
			}
			args := append(candidateRefs("1.2.3"), "-pi-tarball", tarball)
			coreVersion := "1.2.3"
			if name == "independent versions" {
				coreVersion = "3.0.0"
				args[1] = "refs/tags/v" + coreVersion
			}
			out, diagnostic, code := candidateRun(t, root, args...)
			if name == "valid" || name == "independent versions" || name == "legacy regular file type" || name == "matching large manifest integer" {
				if code != 0 || diagnostic != "" {
					t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, diagnostic)
				}
				if out != "{\"version\":\""+coreVersion+"\",\"piVersion\":\"1.2.3\",\"validation\":\"local-package-only\",\"files\":10}\n" {
					t.Fatalf("unexpected success: %q", out)
				}
			} else if code != 1 || out != "" || diagnostic != "Pi package tarball validation failed\nexit status 1\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
		})
	}
}
