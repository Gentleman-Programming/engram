//go:build !windows

package main

func prepareSQLiteTemp(string) (func(), error) {
	return func() {}, nil
}
