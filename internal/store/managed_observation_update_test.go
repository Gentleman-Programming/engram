package store

import (
	"errors"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
)

func TestManagedRuntimeUpdateSurvivesLostResponse(t *testing.T) {
	cfg := FallbackConfig(t.TempDir())
	st, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	scope := ManagedRuntimeScope{
		Root: "managed-update", Project: "project", Directory: project.RuntimeWorktreeDirectory(t.TempDir()),
		Mode: SessionOwnershipProjectOwned,
	}
	scope.EffectiveID, _, err = st.AttachManagedRuntime(scope, managedTestHostA, false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.AddObservation(AddObservationParams{
		SessionID: scope.EffectiveID, Project: scope.Project, Title: "before", Content: "needle",
	})
	if err != nil {
		t.Fatal(err)
	}
	find, replace := "needle", "needle+"
	request := ManagedObservationUpdate{ID: id, ExpectedProject: scope.Project, Changes: UpdateObservationParams{Find: &find, Replace: &replace}}
	generation, _, err := st.ListManagedRuntimeHosts(scope)
	if err != nil {
		t.Fatal(err)
	}
	prior := AddObservationParams{Title: "earlier save", Content: "queued"}
	if err := st.RecordManagedRuntimeObservation(scope, generation, "prior-save", prior); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateManagedRuntimeObservation(t.Context(), scope, managedTestHostA, "edit", request); !errors.Is(err, ErrManagedRuntimeWriteUnresolved) {
		t.Fatalf("update overtook an outstanding save: %v", err)
	}
	prior.OperationID = "prior-save"
	if _, err := st.SaveManagedRuntimeObservation(scope, generation, prior); err != nil {
		t.Fatal(err)
	}
	if err := st.StartSessionWithOwnershipMode("foreign", "other", scope.Directory, scope.Mode); err != nil {
		t.Fatal(err)
	}
	foreign, err := st.AddObservation(AddObservationParams{SessionID: "foreign", Project: "other", Title: "Unrelated", Content: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	wrong := request
	wrong.ID = foreign
	if _, err := st.UpdateManagedRuntimeObservation(t.Context(), scope, managedTestHostA, "edit", wrong); !errors.Is(err, ErrObservationProjectMismatch) {
		t.Fatalf("updated another project's observation: %v", err)
	}
	// A failed edit cannot keep the inserted receipt or reserve its key.
	if got, err := st.UpdateManagedRuntimeObservation(t.Context(), scope, managedTestHostA, "edit", request); err != nil || got.Replayed {
		t.Fatalf("first update = %+v, %v", got, err)
	}
	original := "a full replacement <private>first secret</private>"
	secondID, err := st.AddObservation(AddObservationParams{SessionID: scope.EffectiveID, Project: scope.Project, Title: "second", Content: "before"})
	if err != nil {
		t.Fatal(err)
	}
	contentEdit := ManagedObservationUpdate{ID: secondID, ExpectedProject: scope.Project, Changes: UpdateObservationParams{Content: &original}}
	first, err := st.UpdateManagedRuntimeObservation(t.Context(), scope, managedTestHostA, "full-edit", contentEdit)
	if err != nil || first.Truncation == nil || first.Truncation.Truncated {
		t.Fatalf("original edit metadata: %+v %v", first, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.MaxObservationLength = 3
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got, err := reopened.UpdateManagedRuntimeObservation(t.Context(), scope, managedTestHostA, "edit", request); err != nil || !got.Replayed {
		t.Fatalf("replay after reopen = %+v, %v", got, err)
	}
	redactedRetry := "a full replacement <private>different secret</private>"
	contentEdit.Changes.Content = &redactedRetry
	if got, err := reopened.UpdateManagedRuntimeObservation(t.Context(), scope, managedTestHostA, "full-edit", contentEdit); err != nil || !got.Replayed ||
		got.Truncation == nil || *got.Truncation != *first.Truncation {
		t.Fatalf("replay changed the recorded content limit: %+v %v", got, err)
	}
	if _, err := reopened.UpdateManagedRuntimeObservation(t.Context(), scope, managedTestHostA, "new-edit", request); !errors.Is(err, ErrObservationFindReplaceInputTooLarge) {
		t.Fatalf("new edit bypassed the current content limit: %v", err)
	}
	obs, err := reopened.GetObservation(id)
	if err != nil || obs.Content != "needle+" || obs.RevisionCount != 2 {
		t.Fatalf("unexpected edited observation: %+v, %v", obs, err)
	}
	history, err := reopened.GetObservationVersions(id)
	if err != nil || len(history) != 1 || history[0].Content != "needle" {
		t.Fatalf("unexpected edit history: %+v, %v", history, err)
	}
	if id, err := reopened.GetObservationSaveResult("edit"); id != 0 || err != nil {
		t.Fatalf("an edit receipt appeared as a save: %d %v", id, err)
	}
	if _, err := reopened.AddObservationWithResult(AddObservationParams{SessionID: scope.EffectiveID, Project: scope.Project, Title: "Unrelated save",
		Content: "save", OperationID: "edit"}); !errors.Is(err, ErrObservationOperationConflict) {
		t.Fatalf("ordinary save reused an edit operation ID: %v", err)
	}
}
