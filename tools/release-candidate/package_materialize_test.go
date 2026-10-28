package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPackageMaterialization(t *testing.T) {
	for _, scenario := range []string{"valid", "preexisting", "content", "unsafe", "link", "duplicate", "missing", "malformed", "incomplete", "crc"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			base := filepath.Join(root, "plugin", "pi")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			names := append(append([]string{}, packageRuntimeFiles...), "assets/nested.txt")
			manifest, err := json.Marshal(map[string]any{"name": "gentle-engram", "version": "1.2.3", "files": names})
			if err != nil {
				t.Fatal(err)
			}
			source := map[string]string{}
			for _, name := range names {
				body := "fixture bytes: " + name
				if name == "package.json" {
					body = string(manifest)
				}
				source[name] = body
				filename := filepath.Join(base, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// npm's equivalent JSON formatting must survive extraction byte-for-byte.
			packed := map[string]string{}
			for name, body := range source {
				packed[name] = body
			}
			packed["package.json"] = "\n" + string(manifest) + "\n"
			var buffer bytes.Buffer
			gz := gzip.NewWriter(&buffer)
			tw := tar.NewWriter(gz)
			write := func(name, body string, kind byte) {
				t.Helper()
				h := &tar.Header{Name: "package/" + name, Mode: 0644, Size: int64(len(body)), Typeflag: kind}
				if kind == tar.TypeSymlink {
					h.Size = 0
					h.Linkname = "index.ts"
				}
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if h.Size > 0 {
					if _, err := tw.Write([]byte(body)); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, name := range names {
				if scenario == "missing" && name == names[len(names)-1] {
					continue
				}
				body, kind, entryName := packed[name], byte(tar.TypeReg), name
				if name == "assets/nested.txt" {
					switch scenario {
					case "content":
						body = strings.Repeat("x", len(body))
					case "unsafe":
						entryName = "../escape"
					case "link":
						kind = tar.TypeSymlink
					}
				}
				write(entryName, body, kind)
			}
			if scenario == "duplicate" {
				write("index.ts", packed["index.ts"], tar.TypeReg)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			archive := buffer.Bytes()
			switch scenario {
			case "malformed":
				archive = []byte("invalid gzip")
			case "incomplete":
				archive = archive[:len(archive)/2]
			case "crc":
				archive[len(archive)-8] ^= 1
			}
			tarball := filepath.Join(root, "candidate.tgz")
			if err := os.WriteFile(tarball, archive, 0600); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, "staging")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(parent, "keep")
			if err := os.WriteFile(sentinel, []byte("parent data"), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(parent, "package")
			if scenario == "preexisting" {
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(destination, "original"), []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func(dir string) map[string]string {
				t.Helper()
				result := map[string]string{}
				err := filepath.WalkDir(dir, func(filename string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						return nil
					}
					data, err := os.ReadFile(filename)
					if err != nil {
						return err
					}
					rel, err := filepath.Rel(dir, filename)
					if err != nil {
						return err
					}
					result[filepath.ToSlash(rel)] = string(data)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			before := snapshot(root)
			count, err := validatePackageInto(root, "1.2.3", tarball, destination)
			if scenario == "valid" {
				if err != nil || count != len(names) {
					t.Fatalf("count=%d error=%v", count, err)
				}
				if _, err := os.Stat(destination); err != nil {
					t.Fatalf("materialization absent: %v", err)
				}
				if got := snapshot(destination); !reflect.DeepEqual(got, packed) {
					t.Fatalf("archive bytes mismatch: %v", got)
				}
				after := snapshot(root)
				for name := range after {
					if strings.HasPrefix(name, "staging/package/") {
						delete(after, name)
					}
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("source, archive or parent changed")
				}
			} else {
				if !errors.Is(err, errPackageRejected) || count != 0 {
					t.Errorf("rejection required: count=%d error=%v", count, err)
				}
				if scenario != "preexisting" {
					if _, err := os.Lstat(destination); !os.IsNotExist(err) {
						t.Errorf("partial output remains: %v", err)
					}
				}
				if !reflect.DeepEqual(before, snapshot(root)) {
					t.Error("source, archive or parent changed")
				}
			}
		})
	}
}
