package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const projectAliasUsage = "usage: engram projects alias set (--key <key>|--cwd <directory>) --project <project>\n       engram projects alias list\n       engram projects alias remove (--key <key>|--cwd <directory>)\n       engram projects alias resolve [--designation <name>] [--cwd <directory>]\n"

// Alias administration is local and future-only. Resolve previews derivation;
// it must not enroll, initialize a Git binding, or change delivery state.
func cmdProjectsAlias(cfg store.Config) {
	if err := runProjectsAlias(cfg, os.Args[3:]); err != nil {
		fmt.Fprintf(os.Stderr, "project alias: %s\n", err)
		exitFunc(1)
	}
}

func runProjectsAlias(cfg store.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected set, list, remove, or resolve")
	}
	if args[0] == "--help" && len(args) == 1 {
		fmt.Print(projectAliasUsage)
		return nil
	}
	action := args[0]
	allowed := map[string]bool{}
	switch action {
	case "set":
		allowed = map[string]bool{"--key": true, "--cwd": true, "--project": true}
	case "remove":
		allowed = map[string]bool{"--key": true, "--cwd": true}
	case "resolve":
		allowed = map[string]bool{"--designation": true, "--cwd": true}
	case "list":
	default:
		return fmt.Errorf("unknown command %q", action)
	}
	flags := map[string]string{}
	for i := 1; i < len(args); i++ {
		name := args[i]
		if !allowed[name] {
			return fmt.Errorf("unexpected argument %q", name)
		}
		if _, ok := flags[name]; ok {
			return fmt.Errorf("duplicate flag %s", name)
		}
		if i+1 == len(args) || len(args[i+1]) == 0 || (len(args[i+1]) >= 2 && args[i+1][:2] == "--") {
			return fmt.Errorf("%s requires a value", name)
		}
		i++
		flags[name] = args[i]
	}
	kind := store.ProjectAliasKindKey
	key := flags["--key"]
	if action == "set" || action == "remove" {
		_, hasKey := flags["--key"]
		_, hasCWD := flags["--cwd"]
		if hasKey == hasCWD {
			return fmt.Errorf("exactly one of --key or --cwd is required")
		}
		if hasCWD {
			kind = store.ProjectAliasKindCWD
			key = flags["--cwd"]
		}
		if action == "set" && flags["--project"] == "" {
			return fmt.Errorf("--project is required")
		}
	}
	var candidate project.RoutingCandidate
	if action == "resolve" {
		cwd := flags["--cwd"]
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		root := ""
		if flags["--designation"] == "" {
			root = project.ObserveGitRoot(cwd)
		}
		var err error
		candidate, err = project.DeriveRoutingCandidate(project.RoutingCandidateInput{Designation: flags["--designation"], GitRoot: root, Directory: cwd})
		if err != nil {
			return err
		}
	}
	s, err := storeNew(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	switch action {
	case "set":
		alias, err := s.SetProjectAlias(kind, key, flags["--project"])
		if err != nil {
			return err
		}
		fmt.Printf("Alias set: %s %s -> %s\n", alias.Kind, alias.Key, alias.Project)
	case "remove":
		removed, err := s.RemoveProjectAlias(kind, key)
		if err != nil {
			return err
		}
		if removed {
			fmt.Println("Alias removed.")
		} else {
			fmt.Println("Alias not found.")
		}
	case "list":
		aliases, err := s.ListProjectAliases()
		if err != nil {
			return err
		}
		if len(aliases) == 0 {
			fmt.Println("No project aliases.")
		}
		for _, alias := range aliases {
			fmt.Printf("%s\t%s\t%s\n", alias.Kind, alias.Key, alias.Project)
		}
	case "resolve":
		aliases, err := s.ResolveProjectAlias(candidate.Key, candidate.Directory)
		if err != nil {
			return err
		}
		enrolled := false
		if !aliases.Conflict && aliases.Project != "" {
			enrolled, err = s.IsProjectEnrolled(aliases.Project)
			if err != nil {
				return err
			}
		}
		disposition := "inbox"
		target := candidate.Key
		if target == "" {
			target = "unsorted"
		}
		if enrolled {
			disposition = "enrolled"
			target = aliases.Project
		}
		result := struct {
			Candidate   project.RoutingCandidate      `json:"candidate"`
			Aliases     *store.ProjectAliasResolution `json:"aliases"`
			Conflict    bool                          `json:"conflict"`
			Enrolled    bool                          `json:"enrolled"`
			Disposition string                        `json:"disposition"`
			Project     string                        `json:"project"`
		}{candidate, aliases, aliases.Conflict, enrolled, disposition, target}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	return nil
}
