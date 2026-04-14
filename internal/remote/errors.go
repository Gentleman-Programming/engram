// Package remote implements the cloud client for engram — both the HTTP client
// wrapper, configuration management, SyncClient (local-first), and RemoteStore
// (cloud-only mode implementing StoreInterface).
package remote

import "errors"

var (
	// ErrUnauthorized is returned when the API key is invalid or missing.
	ErrUnauthorized = errors.New("unauthorized")

	// ErrNotFound is returned when the requested resource does not exist.
	ErrNotFound = errors.New("not found")

	// ErrRateLimited is returned when the server returns 429 Too Many Requests.
	ErrRateLimited = errors.New("rate limited")

	// ErrServerError is returned when the server returns a 5xx status code.
	ErrServerError = errors.New("server error")

	// ErrConfigNotFound is returned when no cloud config exists in the store.
	ErrConfigNotFound = errors.New("cloud config not found")
)
