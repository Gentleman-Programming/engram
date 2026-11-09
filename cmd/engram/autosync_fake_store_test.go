package main

import (
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestAutosyncFakeStorePaginationConcurrentWrite(t *testing.T) {
	s := newAutosyncFakeStore()
	writerLocked, releaseWriter, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWriter) }) }
	s.beforeAppend = func() {
		close(writerLocked)
		<-releaseWriter
	}
	go func() {
		defer close(writerDone)
		s.addMutation("controlled-write")
	}()
	type readResult struct {
		rows []store.SyncMutation
		err  error
	}
	readerDone := make(chan readResult, 1)
	readerStarted, readerJoined := false, false
	defer func() {
		release()
		<-writerDone
		if readerStarted && !readerJoined {
			<-readerDone
		}
	}()
	select {
	case <-writerLocked:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not acquire its mutex")
	}
	readerStarted = true
	go func() {
		rows, err := s.ListPendingSyncMutationsAfterSeq("cloud", 0, 2)
		readerDone <- readResult{rows, err}
	}()

	// Observe the blocked call without ordering its earlier argument evaluation
	// before the writer through a synchronization channel.
	deadline := time.Now().Add(2 * time.Second)
	blocked := false
	buf := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, ".ListPendingSyncMutationsAfterSeq(") &&
				strings.Contains(stack, ".ListPendingSyncMutations(") &&
				strings.Contains(stack, "runtime_SemacquireMutex") {
				blocked = true
				break
			}
		}
		if blocked {
			break
		}
		runtime.Gosched()
	}
	if !blocked {
		t.Fatal("reader did not reach the snapshot lock while the writer owned it")
	}
	release()
	<-writerDone
	var got readResult
	select {
	case got = <-readerDone:
		readerJoined = true
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not return after the writer released its mutex")
	}
	if got.err != nil {
		t.Fatalf("paginate: %v", got.err)
	}
	want, err := s.ListPendingSyncMutations("cloud", 0)
	if err != nil || len(want) != 1 || want[0].Seq != 1 || want[0].EntityKey != "controlled-write" || !reflect.DeepEqual(got.rows, want) {
		t.Fatalf("snapshot mismatch: got=%+v want=%+v err=%v", got.rows, want, err)
	}
}

func TestAutosyncFakeStorePaginationPreservesCursor(t *testing.T) {
	s := newAutosyncFakeStore()
	for _, key := range []string{"first", "second", "third"} {
		s.addMutation(key)
	}
	before, err := s.ListPendingSyncMutations("cloud", 0)
	if err != nil || len(before) != 3 {
		t.Fatalf("initial snapshot: rows=%+v err=%v", before, err)
	}
	got, err := s.ListPendingSyncMutationsAfterSeq("cloud", 1, 1)
	if err != nil || !reflect.DeepEqual(got, before[1:2]) {
		t.Fatalf("later page: got=%+v want=%+v err=%v", got, before[1:2], err)
	}
	after, err := s.ListPendingSyncMutations("cloud", 0)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("pagination changed stored mutations: before=%+v after=%+v err=%v", before, after, err)
	}
}
