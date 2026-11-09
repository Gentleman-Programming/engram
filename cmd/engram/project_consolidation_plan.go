package main

import (
	"fmt"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// projectConsolidationPlan describes a confirmed selection, not permission to
// mutate. The command owns prompts/output; the Store revalidates every merge.
type projectConsolidationPlan struct {
	canonical, renameTarget string
	sources                 []string
	invalidSelections       []string
}

// Callers handle input normalization and cancellation before planning. A bad
// detected-project selection exposes no sources, preserving all-or-nothing
// selection. Grouped selection below deliberately keeps its partial policy.
func planDetectedProjectConsolidation(canonical string, candidates []project.ProjectMatch, answer string) projectConsolidationPlan {
	plan := projectConsolidationPlan{canonical: canonical}
	if answer == "all" || answer == "a" {
		for _, candidate := range candidates {
			plan.sources = append(plan.sources, candidate.Name)
		}
		return plan
	}
	for _, part := range strings.Split(answer, ",") {
		part = strings.TrimSpace(part)
		idx := 0
		if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(candidates) {
			return projectConsolidationPlan{canonical: canonical, invalidSelections: []string{part}}
		}
		plan.sources = append(plan.sources, candidates[idx-1].Name)
	}
	return plan
}

func planGroupedProjectConsolidation(group projectGroup, answer, renameTarget string) projectConsolidationPlan {
	canonical, _ := store.NormalizeProject(group.Canonical)
	plan := projectConsolidationPlan{canonical: canonical, renameTarget: renameTarget}
	if answer == "all" || answer == "a" || answer == "y" || answer == "yes" {
		for _, name := range group.Names {
			if name != canonical {
				plan.sources = append(plan.sources, name)
			}
		}
		return plan
	}
	for _, part := range strings.Split(answer, ",") {
		part = strings.TrimSpace(part)
		idx := 0
		if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 1 || idx > len(group.Names) {
			plan.invalidSelections = append(plan.invalidSelections, part)
			continue
		}
		if selected := group.Names[idx-1]; selected != canonical {
			plan.sources = append(plan.sources, selected)
		}
	}
	return plan
}
