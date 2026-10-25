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
	first := &r.Cases[0]
	first.PiRuntime = &artifactPiCheck{Name: "pi-workflow-topics", State: "unavailable", Cleanup: "not-started", Joined: true}
	first.CoreRuntime = runArtifactPiCase(ctx, core, digest, run, materialize, first.PiRuntime, "topics")
	if first.CoreRuntime.State == "passed" && first.PiRuntime.State == "passed" {
		first.State = "passed"
	}
	return r // Remaining four families are not implemented or qualified yet.
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
