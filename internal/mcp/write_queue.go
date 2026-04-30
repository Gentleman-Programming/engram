package mcp

import (
	"context"
	"errors"
	"fmt"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const defaultMCPWriteQueueSize = 32

var errMCPWriteQueueFull = errors.New("engram: mcp write queue is full; retry shortly")

type writeQueue struct {
	jobs chan writeJob
}

type writeJob struct {
	ctx    context.Context
	run    func(context.Context) (*mcppkg.CallToolResult, error)
	result chan writeResult
}

type writeResult struct {
	result *mcppkg.CallToolResult
	err    error
}

func newWriteQueue(size int) *writeQueue {
	if size <= 0 {
		size = defaultMCPWriteQueueSize
	}
	q := &writeQueue{jobs: make(chan writeJob, size)}
	go q.run()
	return q
}

func (q *writeQueue) run() {
	for job := range q.jobs {
		if err := job.ctx.Err(); err != nil {
			job.result <- writeResult{err: err}
			continue
		}

		result, err := runWriteJob(job)
		job.result <- writeResult{result: result, err: err}
	}
}

func runWriteJob(job writeJob) (result *mcppkg.CallToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("mcp write handler panic: %v", recovered)
		}
	}()

	return job.run(job.ctx)
}

func (q *writeQueue) Do(ctx context.Context, run func(context.Context) (*mcppkg.CallToolResult, error)) (*mcppkg.CallToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	job := writeJob{
		ctx:    ctx,
		run:    run,
		result: make(chan writeResult, 1),
	}

	select {
	case q.jobs <- job:
		// Enqueued.
	default:
		return nil, errMCPWriteQueueFull
	}

	// The worker owns the post-enqueue cancellation decision. Returning directly
	// on ctx.Done() here can race with the worker starting the job: the caller may
	// see cancellation while the handler is about to mutate SQLite. Waiting for the
	// worker's result makes the outcome deterministic: queued canceled jobs are
	// skipped by the worker before start, while started jobs finish and return the
	// handler result.
	res := <-job.result
	return res.result, res.err
}

func queuedWriteHandler(q *writeQueue, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcppkg.CallToolRequest) (*mcppkg.CallToolResult, error) {
		result, err := q.Do(ctx, func(runCtx context.Context) (*mcppkg.CallToolResult, error) {
			return h(runCtx, req)
		})
		if err == nil {
			return result, nil
		}
		if errors.Is(err, errMCPWriteQueueFull) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return mcppkg.NewToolResultError(fmt.Sprintf("MCP write queue error: %s", err)), nil
		}
		return nil, err
	}
}
