package main_test

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPiSourceFixtureDelta(t *testing.T) {
	// Synthetic formatter controls, not a reproduction of the Linux mutation.
	root := filepath.Join(t.TempDir(), "fixture")
	path := func(name string) string { return filepath.Join(root, name) }
	before := map[string]string{path("z-modified"): "old private content", path("a-deleted"): "deleted private content", path("same"): "same"}
	after := map[string]string{path("z-modified"): "new private content", path("m-added"): "added private content", path("same"): "same"}
	beforeCopy, afterCopy := map[string]string{}, map[string]string{}
	for k, v := range before {
		beforeCopy[k] = v
	}
	for k, v := range after {
		afterCopy[k] = v
	}
	metadata := func(s string) string { return fmt.Sprintf("sha256=%x size=%d", sha256.Sum256([]byte(s)), len(s)) }
	want := "delete a-deleted before=" + metadata(before[path("a-deleted")]) + " after=absent\n" +
		"add m-added before=absent after=" + metadata(after[path("m-added")]) + "\n" +
		"modify z-modified before=" + metadata(before[path("z-modified")]) + " after=" + metadata(after[path("z-modified")]) + "\n"
	t.Run("sorted relative metadata only", func(t *testing.T) {
		for i := 0; i < 8; i++ {
			got := piSourceFixtureDelta(root, before, after)
			if got != want {
				t.Errorf("formatter output = %q; want %q", got, want)
			}
			if strings.Contains(got, root) || strings.Contains(got, "private content") || strings.Contains(got, "same") {
				t.Fatalf("diagnostic exposed root, contents or unchanged file: %q", got)
			}
		}
	})
	t.Run("unchanged and empty", func(t *testing.T) {
		if got := piSourceFixtureDelta(root, before, before); got != "" {
			t.Fatalf("unchanged delta = %q", got)
		}
		if got := piSourceFixtureDelta(root, nil, nil); got != "" {
			t.Fatalf("empty delta = %q", got)
		}
	})
	if !reflect.DeepEqual(before, beforeCopy) || !reflect.DeepEqual(after, afterCopy) {
		t.Fatal("formatter mutated input maps")
	}
}
