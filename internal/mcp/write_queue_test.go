package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestWriteQueueConcurrentWritesAreSerialized(t *testing.T) {
	q := newWriteQueue(32)

	const writes = 12
	var active int32
	var maxActive int32
	var wg sync.WaitGroup
	errCh := make(chan error, writes)

	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := q.Do(context.Background(), func(context.Context) (*mcppkg.CallToolResult, error) {
				current := atomic.AddInt32(&active, 1)
				for {
					seen := atomic.LoadInt32(&maxActive)
					if current <= seen || atomic.CompareAndSwapInt32(&maxActive, seen, current) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return mcppkg.NewToolResultText(fmt.Sprintf("write-%d", i)), nil
			})
			if err != nil {
				errCh <- err
				return
			}
			if res == nil || res.IsError {
				errCh <- fmt.Errorf("unexpected result: %#v", res)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("expected serialized writes with max active=1, got %d", got)
	}
}

func TestWriteQueueFullAndCanceledQueuedWriteDoNotRun(t *testing.T) {
	q := newWriteQueue(1)
	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})

	firstDone := make(chan error, 1)
	go func() {
		_, err := q.Do(context.Background(), func(context.Context) (*mcppkg.CallToolResult, error) {
			close(firstStarted)
			<-releaseFirst
			return mcppkg.NewToolResultText("first"), nil
		})
		firstDone <- err
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first write did not start")
	}

	ctxSecond, cancelSecond := context.WithCancel(context.Background())
	var secondRan atomic.Bool
	secondDone := make(chan error, 1)
	go func() {
		_, err := q.Do(ctxSecond, func(context.Context) (*mcppkg.CallToolResult, error) {
			secondRan.Store(true)
			return mcppkg.NewToolResultText("second"), nil
		})
		secondDone <- err
	}()

	waitForQueueDepth(t, q, 1)

	_, err := q.Do(context.Background(), func(context.Context) (*mcppkg.CallToolResult, error) {
		return mcppkg.NewToolResultText("third"), nil
	})
	if !errors.Is(err, errMCPWriteQueueFull) {
		t.Fatalf("expected queue full error, got %v", err)
	}

	cancelSecond()
	close(releaseFirst)

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first write failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first write did not complete")
	}

	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled queued write, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second write did not return cancellation")
	}

	if secondRan.Load() {
		t.Fatal("canceled queued write ran after cancellation")
	}
}

func TestWriteQueueStartedCanceledWriteReturnsHandlerResult(t *testing.T) {
	q := newWriteQueue(1)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})

	done := make(chan struct {
		res *mcppkg.CallToolResult
		err error
	}, 1)
	go func() {
		res, err := q.Do(ctx, func(context.Context) (*mcppkg.CallToolResult, error) {
			close(started)
			<-release
			return mcppkg.NewToolResultText("committed"), nil
		})
		done <- struct {
			res *mcppkg.CallToolResult
			err error
		}{res: res, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}

	cancel()
	close(release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("expected handler result after started cancellation, got error %v", got.err)
		}
		if got.res == nil || got.res.IsError || callResultText(t, got.res) != "committed" {
			t.Fatalf("unexpected result after started cancellation: %#v", got.res)
		}
	case <-time.After(time.Second):
		t.Fatal("started canceled write did not return handler result")
	}
}

func TestWriteQueuePanicDoesNotKillWorker(t *testing.T) {
	q := newWriteQueue(1)

	_, err := q.Do(context.Background(), func(context.Context) (*mcppkg.CallToolResult, error) {
		panic("boom")
	})
	if err == nil || !strings.Contains(err.Error(), "panic") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected panic error, got %v", err)
	}

	res, err := q.Do(context.Background(), func(context.Context) (*mcppkg.CallToolResult, error) {
		return mcppkg.NewToolResultText("after panic"), nil
	})
	if err != nil {
		t.Fatalf("worker did not process next write after panic: %v", err)
	}
	if res == nil || res.IsError || callResultText(t, res) != "after panic" {
		t.Fatalf("unexpected next write result after panic: %#v", res)
	}
}

func TestReadHandlerDoesNotWaitBehindBlockedQueuedWrite(t *testing.T) {
	q := newWriteQueue(1)
	release := make(chan struct{})
	started := make(chan struct{})

	writeH := queuedWriteHandler(q, func(context.Context, mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		close(started)
		<-release
		return mcppkg.NewToolResultText("write done"), nil
	})

	writeDone := make(chan error, 1)
	go func() {
		_, err := writeH(context.Background(), mcppkg.CallToolRequest{})
		writeDone <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}

	readDone := make(chan error, 1)
	go func() {
		res, err := handleSuggestTopicKey()(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title": "Queue read isolation",
		}}})
		if err != nil {
			readDone <- err
			return
		}
		if res.IsError || !strings.Contains(callResultText(t, res), "queue-read-isolation") {
			readDone <- fmt.Errorf("unexpected read result: %s", callResultText(t, res))
			return
		}
		readDone <- nil
	}()

	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("read handler waited behind blocked queued write")
	}

	close(release)
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write handler failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write handler did not complete")
	}
}

func TestQueuedHandleSavePersistsMemory(t *testing.T) {
	s := newMCPTestStore(t)
	h := queuedWriteHandler(newWriteQueue(1), handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute)))

	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Queued save architecture",
		"content": "MCP writes are serialized through an explicit queue",
		"type":    "architecture",
	}}})
	if err != nil {
		t.Fatalf("queued save handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("queued save returned tool error: %s", callResultText(t, res))
	}

	env := parseEnvelope(t, "queued save", res)
	project, _ := env["project"].(string)
	if project == "" {
		t.Fatalf("queued save response missing project: %v", env)
	}

	obs, err := s.RecentObservations(project, "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	for _, o := range obs {
		if o.Title == "Queued save architecture" {
			return
		}
	}
	t.Fatalf("queued save did not persist expected observation; got %+v", obs)
}

func waitForQueueDepth(t *testing.T, q *writeQueue, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if len(q.jobs) == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("queue depth did not reach %d; got %d", want, len(q.jobs))
		case <-ticker.C:
		}
	}
}
