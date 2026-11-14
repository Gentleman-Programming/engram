package main

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/pe"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStableTag(t *testing.T) {
	for _, tag := range []string{"v0.0.0", "v3.7.2", "v65535.65535.65535"} {
		if _, err := parseTag(tag); err != nil {
			t.Errorf("%s: %v", tag, err)
		}
	}
	for _, tag := range []string{"", "3.7.2", " v3.7.2", "v03.7.2", "v3.7", "v3.7.2.0", "v3.7.2-rc.1", "v3.7.2+build", "v65536.0.0", "v1.-1.0", "v1.2.٣"} {
		if _, err := parseTag(tag); err == nil {
			t.Errorf("accepted %q", tag)
		}
	}
}

func TestGenerateRefusesExistingDirectory(t *testing.T) {
	v, _ := parseTag("v3.7.2")
	root := t.TempDir()
	sentinel := filepath.Join(root, "rsrc_windows_amd64.syso")
	if err := os.WriteFile(sentinel, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := generate(v, root); err == nil {
		t.Fatal("accepted existing staging directory")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "stale" {
		t.Fatal("modified stale resource")
	}
	out := filepath.Join(root, "fresh")
	if err := generate(v, out); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		file, err := pe.Open(filepath.Join(out, "rsrc_windows_"+arch+".syso"))
		if err != nil {
			t.Fatal(err)
		}
		m, _ := machine(arch)
		if file.Machine != m || len(file.Sections) != 1 || len(file.Sections[0].Relocs) != 1 {
			t.Fatal("wrong COFF structure")
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCLI(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		nil, {"unknown"}, {"generate", "-tag", "v3.7.2-rc.1", "-out", filepath.Join(root, "prerelease")},
		{"generate", "-tag", "v3.7.2"}, {"generate", "-tag", "v3.7.2", "unexpected"},
		{"verify", "-tag", "v3.7.2", "-arch", "386", "-file", "missing"},
		{"verify", "-tag", "v3.7.2", "-arch", "amd64", "-file", filepath.Join(root, "missing")},
		{"generate", "-unknown"},
	} {
		var stdout, stderr bytes.Buffer
		if run(args, &stdout, &stderr) == 0 || stderr.Len() == 0 || stdout.Len() != 0 {
			t.Errorf("accepted %q", args)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "prerelease")); !os.IsNotExist(err) {
		t.Fatal("created directory for invalid tag")
	}
	for _, action := range []string{"generate", "verify"} {
		if run([]string{action, "-help"}, &bytes.Buffer{}, &bytes.Buffer{}) != 0 {
			t.Fatal("help failed")
		}
	}
	if run([]string{"generate", "-tag", "v65535.65535.65535", "-out", filepath.Join(root, "max")}, &bytes.Buffer{}, &bytes.Buffer{}) != 0 {
		t.Fatal("valid generation failed")
	}
}

func buildFixture(t *testing.T, dir, target, arch, out string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, ".")
	cmd.Dir = dir
	overrides := map[string]string{"GOOS": target, "GOARCH": arch, "CGO_ENABLED": "0", "GOWORK": "off", "GOFLAGS": "", "GOENV": "off", "GOTOOLCHAIN": "local"}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range overrides {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture build %s/%s: %v\n%s", target, arch, err, output)
	}
}

func writeFixtureSource(t *testing.T, dir string) {
	t.Helper()
	for name, content := range map[string]string{"main.go": "package main\nfunc main() {}\n", "go.mod": "module synthetic.invalid/metadatafixture\n\ngo 1.26.11\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinkedFixtures(t *testing.T) {
	v, _ := parseTag("v3.7.2")
	root := t.TempDir()
	stage := filepath.Join(root, "source")
	// Baseline before embedding resources: both actual Go PE images lack metadata.
	baseline := filepath.Join(root, "baseline")
	if err := os.Mkdir(baseline, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureSource(t, baseline)
	if err := generate(v, stage); err != nil {
		t.Fatal(err)
	}
	writeFixtureSource(t, stage)
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			before := filepath.Join(root, "before-"+arch+".exe")
			buildFixture(t, baseline, "windows", arch, before)
			if err := verify(v, arch, before); err == nil {
				t.Fatal("baseline unexpectedly has required resource")
			}
			if runtime.GOOS == "windows" {
				assertWindowsMetadata(t, before, false)
			}
			after := filepath.Join(root, "after-"+arch+".exe")
			buildFixture(t, stage, "windows", arch, after)
			var stdout, stderr bytes.Buffer
			if run([]string{"verify", "-tag", "v3.7.2", "-arch", arch, "-file", after}, &stdout, &stderr) != 0 {
				t.Fatalf("linked metadata: %s", &stderr)
			}
			if runtime.GOOS == "windows" {
				assertWindowsMetadata(t, after, true)
			} else {
				t.Log("Windows FileVersionInfo unavailable on this OS; portable PE checks executed")
			}
			testMutations(t, v, arch, after, root)
		})
	}
	linux := filepath.Join(root, "linux")
	buildFixture(t, stage, "linux", "amd64", linux)
	image, err := elf.Open(linux)
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	if image.Machine != elf.EM_X86_64 || image.Section(".rsrc") != nil {
		t.Fatal("Windows resource leaked into Linux build")
	}
}

// This independent Windows reader uses the OS metadata API, not the generator
// or canonical-byte verifier. Neither architecture's executable is launched.
func assertWindowsMetadata(t *testing.T, path string, present bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := `$v=[Diagnostics.FileVersionInfo]::GetVersionInfo($env:ENGRAM_SYNTHETIC_PE); @{Product=$v.ProductName;Version=$v.ProductVersion;FileVersion=$v.FileVersion;Filename=$v.OriginalFilename;FixedProduct=@($v.ProductMajorPart,$v.ProductMinorPart,$v.ProductBuildPart,$v.ProductPrivatePart);FixedFile=@($v.FileMajorPart,$v.FileMinorPart,$v.FileBuildPart,$v.FilePrivatePart)} | ConvertTo-Json -Compress`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "ENGRAM_SYNTHETIC_PE="+path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("independent metadata read: %v\n%s", err, output)
	}
	var info struct {
		Product, Version, FileVersion, Filename string
		FixedProduct, FixedFile                 [4]int
	}
	if err := json.Unmarshal(bytes.TrimPrefix(output, []byte{0xef, 0xbb, 0xbf}), &info); err != nil {
		t.Fatalf("decode metadata: %v: %s", err, output)
	}
	if !present {
		if info.Product != "" || info.Version != "" {
			t.Fatal("baseline unexpectedly has Windows metadata")
		}
		return
	}
	if info.Product != "engram" || info.Version != "3.7.2" || info.FileVersion != "3.7.2" || info.Filename != "engram.exe" || info.FixedProduct != [4]int{3, 7, 2, 0} || info.FixedFile != [4]int{3, 7, 2, 0} {
		t.Fatalf("independent metadata mismatch: %+v", info)
	}
}

func testMutations(t *testing.T, v version, arch, path, root string) {
	t.Helper()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	image, err := pe.NewFile(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	section := image.Section(".rsrc")
	if section == nil {
		t.Fatal("missing linked resource")
	}
	off := int(section.Offset)
	peOffset := int(little.Uint32(original[60:]))
	// PE32+ resource directory is optional header offset 112 + directory #2*8.
	directory := peOffset + 24 + 112 + 16
	mutations := map[string]func([]byte) []byte{
		"wrong-machine":         func(b []byte) []byte { little.PutUint16(b[peOffset+4:], pe.IMAGE_FILE_MACHINE_I386); return b },
		"missing-language":      func(b []byte) []byte { little.PutUint16(b[off+48+14:], 0); return b },
		"duplicate-language":    func(b []byte) []byte { little.PutUint16(b[off+48+14:], 2); return b },
		"extra-type":            func(b []byte) []byte { little.PutUint16(b[off+14:], 2); return b },
		"named-resource":        func(b []byte) []byte { little.PutUint16(b[off+12:], 1); return b },
		"wrong-language":        func(b []byte) []byte { little.PutUint32(b[off+64:], 0x409); return b },
		"invalid-data-rva":      func(b []byte) []byte { little.PutUint32(b[off+72:], 0xffffffff); return b },
		"invalid-directory-rva": func(b []byte) []byte { little.PutUint32(b[directory:], 0xffffffff); return b },
		"oversize-directory":    func(b []byte) []byte { little.PutUint32(b[directory+4:], 0xffffffff); return b },
		"short-virtual-section": func(b []byte) []byte {
			sectionHeaders := peOffset + 24 + int(image.SizeOfOptionalHeader)
			for i, s := range image.Sections {
				if s.Name == ".rsrc" {
					little.PutUint32(b[sectionHeaders+i*40+8:], 1)
				}
			}
			return b
		},
		"resource-outside-image": func(b []byte) []byte {
			little.PutUint32(b[peOffset+24+56:], section.VirtualAddress+1)
			return b
		},
		"fixed-string-disagreement": func(b []byte) []byte { // fixed file version MS is root-aligned value +8
			at := bytes.Index(b[off:], []byte{0xbd, 4, 0xef, 0xfe})
			if at < 0 {
				t.Fatal("missing fixed info signature")
			}
			b[off+at+8] ^= 1
			return b
		},
		"wrong-product": func(b []byte) []byte {
			at := bytes.Index(b[off:], wide("engram"))
			if at < 0 {
				t.Fatal("missing product")
			}
			b[off+at] = 'x'
			return b
		},
		"wrong-string-version": func(b []byte) []byte {
			at := bytes.Index(b[off:], wide("3.7.2"))
			if at < 0 {
				t.Fatal("missing version")
			}
			b[off+at] = '4'
			return b
		},
		"duplicate-string-key": func(b []byte) []byte {
			at := bytes.Index(b[off:], wide("FileVersion"))
			if at < 0 {
				t.Fatal("missing key")
			}
			copy(b[off+at:], wide("ProductName"))
			return b
		},
		"truncated-resource": func(b []byte) []byte { return b[:off+90] },
		"truncated-header":   func(b []byte) []byte { return b[:63] },
		"bad-dos":            func(b []byte) []byte { b[0] = 0; return b },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := mutate(bytes.Clone(original))
			file := filepath.Join(root, "mutated-"+arch+"-"+name+".exe")
			if err := os.WriteFile(file, mutated, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := verify(v, arch, file); err == nil {
				t.Fatal("accepted malformed or mismatched metadata")
			}
		})
	}
	other, _ := parseTag("v3.7.3")
	if err := verify(other, arch, path); err == nil {
		t.Fatal("accepted stale version")
	}
	wrongArch := "amd64"
	if arch == wrongArch {
		wrongArch = "arm64"
	}
	if err := verify(v, wrongArch, path); err == nil {
		t.Fatal("accepted other architecture")
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
}
