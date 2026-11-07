//go:build windows

package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestGenerationStatCapturesIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	writeTestFile(t, path)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, before) {
		t.Fatal("cannot capture original identity")
	}
	info, err := statFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replaceTestFile(t, path)
	if !os.SameFile(before, info) {
		t.Fatal("generation stat deferred identity until after pathname replacement")
	}
	after, err := statFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, after) {
		t.Fatal("replacement reused captured identity")
	}
}

func TestGenerationStatMetadataOnlyAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	writeTestFile(t, path)
	denyGenerationFixtureAccess(t, path, "0x1")
	if f, e := os.Open(path); e == nil {
		_ = f.Close()
		t.Fatal("fixture must deny content reads")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := statFile(path)
	if err != nil {
		t.Fatalf("metadata lookup demanded content access: %v", err)
	}
	if !os.SameFile(before, got) {
		t.Fatal("metadata-only identity mismatch")
	}
}

func TestGenerationStatDeniedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	writeTestFile(t, path)
	denyGenerationFixtureAccess(t, path, "0x81")
	want, baselineErr := os.Stat(path)
	got, err := statFile(path)
	if (err == nil) != (baselineErr == nil) || os.IsPermission(err) != os.IsPermission(baselineErr) {
		t.Fatalf("permission classification changed: got=%v baseline=%v", err, baselineErr)
	}
	if err == nil && os.SameFile(want, want) != os.SameFile(got, got) {
		t.Fatal("denied metadata identity classification changed")
	}
}

func denyGenerationFixtureAccess(t *testing.T, path, mask string) {
	t.Helper()
	original, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(D;;" + mask + ";;;" + user.User.Sid.String() + ")(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		old, _, e := original.DACL()
		if e == nil {
			e = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, old, nil)
		}
		runtime.KeepAlive(original)
		if e != nil {
			t.Errorf("restore owned fixture ACL: %v", e)
		}
	})
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(sd)
}

func TestGenerationStatOpenFileSharing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	writeTestFile(t, path)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	baseline, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := statFile(path)
	if err != nil {
		t.Fatalf("sharing fallback changed Stat error: %v", err)
	}
	if os.SameFile(baseline, baseline) != os.SameFile(got, got) {
		t.Fatal("open-file identity classification differs")
	}
	if os.SameFile(baseline, baseline) && !os.SameFile(baseline, got) {
		t.Fatal("open-file identity differs")
	}
}

func TestGenerationStatAliasesAndLongPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "database.db")
	writeTestFile(t, path)
	want, err := statFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			alias := filepath.Join(root, kind)
			var e error
			if kind == "hardlink" {
				e = os.Link(path, alias)
			} else {
				e = os.Symlink(path, alias)
			}
			if e != nil {
				if kind == "symlink" {
					t.Skipf("symlink privilege unavailable: %v", e)
				}
				t.Fatal(e)
			}
			got, e := statFile(alias)
			if e != nil {
				t.Fatal(e)
			}
			if !os.SameFile(want, got) {
				t.Fatal("alias target identity differs")
			}
		})
	}
	long := filepath.Join(root, strings.Repeat("segment", 20), strings.Repeat("segment", 20))
	if err = os.MkdirAll(long, 0700); err != nil {
		t.Fatal(err)
	}
	long = filepath.Join(long, "database.db")
	writeTestFile(t, long)
	got, err := statFile(long)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := os.Stat(long)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(baseline, got) {
		t.Fatal("long path identity differs")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(wd, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = statFile(relative)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(want, got) {
		t.Fatal("relative path identity differs")
	}
}

func TestGenerationStatPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"database.db", "café 中文.db", filepath.Join("nested", "..", "database.db")} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			writeTestFile(t, path)
			want, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := statFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(want, got) {
				t.Fatal("identity mismatch")
			}
		})
	}
	for _, path := range []string{filepath.Join(root, "missing.db"), "", "invalid\x00path"} {
		_, got := statFile(path)
		_, want := os.Stat(path)
		if got == nil || want == nil || os.IsNotExist(got) != os.IsNotExist(want) {
			t.Fatalf("path=%q got=%v want=%v", path, got, want)
		}
	}
}

func TestGenerationStatClosesHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	writeTestFile(t, path)
	for i := 0; i < 200; i++ {
		if _, err := statFile(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("generation stat left a handle blocking deletion: %v", err)
	}
}
