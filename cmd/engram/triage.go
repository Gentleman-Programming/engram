package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/triage"
)

const (
	triageGitHubAPIBase  = "https://api.github.com/"
	triageExitSuccess    = 0
	triageExitUsage      = 1
	triageRequestTimeout = 2 * time.Minute
)

// runTriageDuplicateDetection and newTriageClient are injectable for tests.
var (
	runTriageDuplicateDetection = triage.Run
	newTriageClient             = func(baseURL, repo, token string) triage.Client {
		return triage.NewRESTClient(baseURL, repo, token, nil)
	}
)

// triageOptions holds the parsed triage-duplicates command line.
type triageOptions struct {
	repo  string
	issue int
}

// cmdTriageDuplicates implements `engram triage-duplicates --repo owner/name
// --issue N`. It is config-free (no store, no autosync): GitHub API and
// network failures are logged as warnings and exit 0 so triage can never
// block or fail a workflow, while usage errors (bad flags, missing token,
// repo, or issue) exit 1.
func cmdTriageDuplicates(args []string) int {
	options, help, err := parseTriageArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		printTriageUsage()
		return triageExitUsage
	}
	if help {
		printTriageUsage()
		return triageExitSuccess
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "error: GITHUB_TOKEN is required")
		printTriageUsage()
		return triageExitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), triageRequestTimeout)
	defer cancel()
	err = runTriageDuplicateDetection(ctx, triage.Options{
		IssueNumber: options.issue,
		Client:      newTriageClient(triageGitHubAPIBase, options.repo, token),
		Log:         log.Printf,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: triage-duplicates failed (ignored): %v\n", err)
		return triageExitSuccess
	}
	return triageExitSuccess
}

// parseTriageArgs parses the triage-duplicates flags: --repo owner/name (or
// the GITHUB_REPOSITORY environment variable), --issue N, --help.
func parseTriageArgs(args []string) (triageOptions, bool, error) {
	var opts triageOptions
	help := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			if i+1 >= len(args) {
				return triageOptions{}, false, fmt.Errorf("--repo requires a value")
			}
			i++
			opts.repo = args[i]
		case "--issue":
			if i+1 >= len(args) {
				return triageOptions{}, false, fmt.Errorf("--issue requires a value")
			}
			i++
			number, err := strconv.Atoi(args[i])
			if err != nil || number <= 0 {
				return triageOptions{}, false, fmt.Errorf("--issue requires a positive issue number, got %q", args[i])
			}
			opts.issue = number
		case "--help", "-h":
			help = true
		default:
			return triageOptions{}, false, fmt.Errorf("unknown triage-duplicates flag %q", args[i])
		}
	}
	if help {
		return triageOptions{}, true, nil
	}
	if opts.repo == "" {
		opts.repo = os.Getenv("GITHUB_REPOSITORY")
	}
	if opts.repo == "" {
		return triageOptions{}, false, fmt.Errorf("--repo owner/name (or GITHUB_REPOSITORY) is required")
	}
	if !validGitHubRepository(opts.repo) {
		return triageOptions{}, false, fmt.Errorf("--repo must be a GitHub owner/repository, got %q", opts.repo)
	}
	if opts.issue == 0 {
		return triageOptions{}, false, fmt.Errorf("--issue is required")
	}
	return opts, help, nil
}

// validGitHubRepository accepts exactly one GitHub-compatible owner/repository
// pair. It intentionally operates on ASCII bytes so whitespace, URL syntax,
// and other unsafe path characters cannot reach REST path construction.
func validGitHubRepository(value string) bool {
	owner, repository, found := strings.Cut(value, "/")
	if !found || strings.Contains(repository, "/") || !validGitHubOwner(owner) {
		return false
	}
	return validGitHubRepositoryName(repository)
}

func validGitHubOwner(owner string) bool {
	if len(owner) < 1 || len(owner) > 39 || !isASCIIAlphanumeric(owner[0]) || !isASCIIAlphanumeric(owner[len(owner)-1]) {
		return false
	}
	for i := 0; i < len(owner); i++ {
		if !isASCIIAlphanumeric(owner[i]) && owner[i] != '-' {
			return false
		}
	}
	return true
}

func validGitHubRepositoryName(repository string) bool {
	if len(repository) < 1 || len(repository) > 100 || repository == "." || repository == ".." {
		return false
	}
	for i := 0; i < len(repository); i++ {
		if !isASCIIAlphanumeric(repository[i]) && repository[i] != '.' && repository[i] != '_' && repository[i] != '-' {
			return false
		}
	}
	return true
}

func isASCIIAlphanumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func printTriageUsage() {
	fmt.Println("usage: engram triage-duplicates --repo owner/name --issue N")
	fmt.Println()
	fmt.Println("Detect possible duplicate issues for one issue and reconcile the")
	fmt.Println("triage:possible-duplicate label and its single anchored comment.")
	fmt.Println("Reads GITHUB_TOKEN from the environment (--repo falls back to")
	fmt.Println("GITHUB_REPOSITORY). GitHub API failures are warnings, not errors.")
}
