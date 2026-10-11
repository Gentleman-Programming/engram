package main

import (
	"encoding/json"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"io"
)

func runCompatibility(args []string, stdout, stderr io.Writer) int {
	var dir string
	jsonFlag := false
	invalid := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if jsonFlag {
				invalid = true
			}
			jsonFlag = true
		case "--data-dir":
			if dir != "" || i+1 == len(args) {
				invalid = true
				break
			}
			i++
			dir = args[i]
		default:
			invalid = true
		}
	}
	if invalid || !jsonFlag || dir == "" {
		fmt.Fprintln(stderr, "Usage: engram compatibility --data-dir <absolute-directory> --json")
		return 2
	}
	result, err := store.ProbeCompatibility(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "compatibility: cannot encode result")
		return 2
	}
	return 0
}
