package identityfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestPublishConcurrentCompleteWinner(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "identity")
	const workers = 16
	contents := strings.Repeat("complete identity\n", 4096)
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		source := filepath.Join(dir, string(rune('a'+i)))
		if err := os.WriteFile(source, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := Publish(source, destination)
			if err == nil || errors.Is(err, fs.ErrExist) {
				data, readErr := os.ReadFile(destination)
				if readErr != nil {
					results <- readErr
					return
				}
				if string(data) != contents {
					results <- errors.New("partial winner visible")
					return
				}
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, fs.ErrExist) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful publications = %d, want 1", winners)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
}

func TestPublishPreservesWinnerAndErrors(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "temporary"), filepath.Join(dir, "identity")
	if err := os.WriteFile(source, []byte("loser"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("winner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(source, destination); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("publication error = %v, want ErrExist", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "winner" {
		t.Fatalf("winner = %q, %v", data, err)
	}
	if err := Publish(filepath.Join(dir, "missing"), filepath.Join(dir, "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing source = %v", err)
	}
}
