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

const help = "Usage: agent-contract [-output path] [-check]\n\nGenerate the Go-owned MCP agent-contract foundation without opening a store.\n  -output path  Artifact path (default docs/contracts/agent-contract-v1.json).\n  -check        Compare without writing; exit 1 for a missing or stale artifact.\n"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent-contract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, help) }
	output := flags.String("output", "docs/contracts/agent-contract-v1.json", "artifact path")
	check := flags.Bool("check", false, "compare without writing")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	if strings.TrimSpace(*output) == "" {
		fmt.Fprintln(stderr, "output path must not be blank")
		return 2
	}
	generated, err := engrammcp.AgentContractJSON()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil {
			fmt.Fprintf(stderr, "read agent contract: %v\n", err)
			return 1
		}
		if !bytes.Equal(current, generated) {
			fmt.Fprintf(stderr, "agent contract is stale: %s; regenerate with go generate ./internal/mcp\n", *output)
			return 1
		}
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		fmt.Fprintf(stderr, "create contract directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*output, generated, 0644); err != nil {
		fmt.Fprintf(stderr, "write agent contract: %v\n", err)
		return 1
	}
	return 0
}
