package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/project"
)

func cmdInit() {
	var projectName string

	if len(os.Args) > 2 {
		projectName = os.Args[2]
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get current working directory: %v\n", err)
		os.Exit(1)
	}

	configDir := filepath.Join(cwd, ".engram")
	configFile := filepath.Join(configDir, "config.json")

	if _, err := os.Stat(configFile); err == nil {
		fmt.Printf("Engram is already initialized in this directory (.engram/config.json exists).\n")
		return
	}

	if projectName == "" {
		fmt.Printf("No git repository detected (or multiple repositories found).\n")
		fmt.Printf("What should we name this project? ")
		
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to read input: %v\n", err)
			os.Exit(1)
		}
		
		projectName = strings.TrimSpace(input)
		if projectName == "" {
			fmt.Println("Project name cannot be empty. Aborting.")
			os.Exit(1)
		}
	}

	// Make sure the project name is valid by normalizing it
	normalizedName := project.DetectProjectFull(cwd).Project
	if normalizedName == "" {
	    // If ambiguous, DetectProjectFull returns empty string for Project, so we fallback
		normalizedName = strings.TrimSpace(strings.ToLower(projectName))
	} else if projectName != "" {
	    normalizedName = strings.TrimSpace(strings.ToLower(projectName))
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create .engram directory: %v\n", err)
		os.Exit(1)
	}

	configData := map[string]string{
		"project_name": normalizedName,
	}

	data, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal config: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write config file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully initialized Engram project '%s' at %s\n", normalizedName, configFile)
}
