// Command agent-contract generates or checks the Go-owned agent contract.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	engrammcp "github.com/Gentleman-Programming/engram/v3/internal/mcp"
)

const help = "Usage: agent-contract [-output path] [-check]\n\nGenerate the Go-owned MCP agent-contract foundation without opening a store.\n  -output path  Artifact path (default docs/contracts/agent-contract-v1.json).\n  -check        Compare without writing; exit 1 for a missing or stale artifact.\n\nVerify Pi model-input projections without writing:\n  agent-contract -verify-pi capture.json\n  Cannot combine with explicitly supplied -output or -check.\n  Exit 0: scope match; 1: refusal; 2: usage or capture unavailable.\n  Not semantic parity, installed-host compatibility or release approval.\n"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent-contract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, help) }
	output := flags.String("output", "docs/contracts/agent-contract-v1.json", "artifact path")
	check := flags.Bool("check", false, "compare without writing")
	verifyPi := flags.String("verify-pi", "", "read-only Pi capture verification")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	supplied := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	if supplied["verify-pi"] {
		if supplied["output"] || supplied["check"] {
			_, _ = fmt.Fprintln(stderr, "verify-pi cannot be combined with output or check")
			return 2
		}
		if strings.TrimSpace(*verifyPi) == "" {
			_, _ = fmt.Fprintln(stderr, "verify-pi path must not be blank")
			return 2
		}
		capture, err := os.ReadFile(*verifyPi)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "Pi input: capture unavailable")
			return 2
		}
		if err := engrammcp.VerifyPiInputSchemas(capture); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if strings.TrimSpace(*output) == "" {
		_, _ = fmt.Fprintln(stderr, "output path must not be blank")
		return 2
	}
	generated, err := engrammcp.AgentContractJSON()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "read agent contract: %v\n", err)
			return 1
		}
		if !bytes.Equal(current, generated) {
			_, _ = fmt.Fprintf(stderr, "agent contract is stale: %s; regenerate with go generate ./internal/mcp\n", *output)
			return 1
		}
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		_, _ = fmt.Fprintf(stderr, "create contract directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*output, generated, 0644); err != nil {
		_, _ = fmt.Fprintf(stderr, "write agent contract: %v\n", err)
		return 1
	}
	return 0
}
