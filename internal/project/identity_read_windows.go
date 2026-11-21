package project

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func readRepositoryBindingFile(path string) ([]byte, error) {
	return readRepositoryBindingFileWithRetry(path, time.Sleep)
}

func readRepositoryBindingFileWithRetry(path string, wait func(time.Duration)) ([]byte, error) {
	data, err := os.ReadFile(path)
	// The winning rename can briefly deny read sharing. Limit the scheduled
	// backoff to 15ms and return other errors immediately.
	for delay := time.Millisecond; delay <= 8*time.Millisecond && errors.Is(err, windows.ERROR_SHARING_VIOLATION); delay *= 2 {
		wait(delay)
		data, err = os.ReadFile(path)
	}
	return data, err
}
