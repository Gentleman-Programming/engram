// Command runtime-contract prints the Go-owned Pi HTTP runtime projection.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
)

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runtime-contract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "check plugin/pi/runtime-contract.json without writing files")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "runtime-contract: unexpected arguments; use -help")
		return 2
	}
	projection, err := server.RuntimeContractProjection()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "runtime-contract: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "runtime-contract: encode projection: %v\n", err)
		return 1
	}
	data = append(data, '\n')
	if *check {
		existing, err := os.ReadFile("plugin/pi/runtime-contract.json")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "runtime-contract: read expected projection: %v\n", err)
			return 1
		}
		if !bytes.Equal(existing, data) {
			_, _ = fmt.Fprintln(stderr, "runtime-contract: expected projection is stale; regenerate with go run ./tools/runtime-contract")
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "Pi HTTP runtime projection is current")
		return 0
	}
	if _, err := stdout.Write(data); err != nil {
		_, _ = fmt.Fprintf(stderr, "runtime-contract: write projection: %v\n", err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
