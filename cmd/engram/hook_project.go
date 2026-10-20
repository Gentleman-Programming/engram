package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"unicode/utf8"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
)

// cmdResolveHookProject is config-free like the other native hook actions. Its
// standalone inspection fits the tightest native prompt budget; native callers
// use InspectLocalProject directly with their own remaining context instead.
func cmdResolveHookProject() {
	ctx, cancel := context.WithTimeout(context.Background(), codexUserPromptDeadline)
	defer cancel()
	output := map[string]string{}
	input, err := io.ReadAll(os.Stdin)
	if err == nil {
		if cwd, ok := hookProjectCWD(input); ok {
			if result, err := projectpkg.InspectLocalProject(ctx, cwd); err == nil {
				output["project"] = result.Project
				output["project_source"] = result.Source
				output["project_path"] = result.Path
			}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		exitFunc(1)
	}
}

// hookProjectCWD accepts existing hook fields but gives identity to exactly one
// lowercase cwd field. A map/struct decoder would silently accept duplicate keys.
func hookProjectCWD(input []byte) (string, bool) {
	if !utf8.Valid(input) {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", false
	}
	var cwd string
	seen := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", false
		}
		if key == "cwd" {
			if seen || json.Unmarshal(value, &cwd) != nil {
				return "", false
			}
			seen = true
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return "", false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", false
	}
	return cwd, seen
}
