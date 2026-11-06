package main

import "context"

type artifactWorkflowObservation struct {
	Name  string                 `json:"name"`
	State string                 `json:"state"`
	Cases []artifactWorkflowCase `json:"cases,omitempty"`
}

type artifactWorkflowCase struct {
	Name        string             `json:"name"`
	State       string             `json:"state"`
	CoreRuntime *artifactCoreCheck `json:"coreRuntime,omitempty"`
	PiRuntime   *artifactPiCheck   `json:"piRuntime,omitempty"`
}

// Keep the original five HTTP assertion families distinct from fixture tests.
func observeArtifactWorkflows(ctx context.Context, core []byte, digest, run string, materialize func(string) bool) *artifactWorkflowObservation {
	r := &artifactWorkflowObservation{Name: "artifact-workflows", State: "unavailable"}
	for _, name := range []string{
		"real Pi topic vectors match Go without registering sessions or creating observations",
		"real Pi consolidation previews without mutation and confirms the exact Go plan",
		"real Pi batch envelopes and ordered item errors leave public state unchanged",
		"real Pi judgments persist successful and mixed outcomes from public scan seeds",
		"real Pi finalization returns Go's original summary receipt and leaves the session ended",
	} {
		r.Cases = append(r.Cases, artifactWorkflowCase{Name: name, State: "unavailable"})
	}
	for i, workflow := range []string{"topics", "consolidation", "batch-errors", "judgments", "finalization"} {
		c := &r.Cases[i]
		c.PiRuntime = &artifactPiCheck{Name: "pi-workflow-" + workflow, State: "unavailable", Cleanup: "not-started", Joined: true}
		c.CoreRuntime = runArtifactPiCase(ctx, core, digest, run, materialize, c.PiRuntime, workflow)
		if c.CoreRuntime.State != "passed" || c.PiRuntime.State != "passed" {
			return r // Never continue after unavailable execution or integrity evidence.
		}
		c.State = "passed"
	}
	r.State = "passed" // All five cases observed; this is never release approval.
	return r
}

func (r *artifactInputObservation) invalidateWorkflowCases() {
	r.Workflows.State = "unavailable"
	for i := range r.Workflows.Cases {
		c := &r.Workflows.Cases[i]
		c.State = "unavailable"
		if c.PiRuntime != nil {
			c.PiRuntime.State = "unavailable"
		}
	}
}
