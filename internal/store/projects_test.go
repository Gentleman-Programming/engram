package store

import (
	"errors"
	"testing"
)

// All tests in this file exercise engram-projects (RFC rfc-engram-projects.md)
// data access against a throwaway store opened with t.TempDir()
// (newProjectsSchemaTestStore, defined in projects_schema_test.go). None of
// them ever touch ~/.engram/engram.db.

func strp(v string) *string { return &v }

func TestUpsertProjectCard_CreateAndIdempotentUpdate(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	card, created, err := s.UpsertProjectCard(UpsertProjectCardParams{Slug: "nextcloud", DisplayName: strp("Nextcloud")})
	if err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on first upsert")
	}
	if card.DisplayName != "Nextcloud" || card.DefaultBranch != "master" || card.JiraProject != "PROJ" {
		t.Fatalf("unexpected defaults: %+v", card)
	}

	// Second upsert with only repo_url set must not touch display_name.
	card2, created2, err := s.UpsertProjectCard(UpsertProjectCardParams{Slug: "nextcloud", RepoURL: strp("https://example/repo")})
	if err != nil {
		t.Fatalf("UpsertProjectCard (update): %v", err)
	}
	if created2 {
		t.Fatal("expected created=false on second upsert")
	}
	if card2.DisplayName != "Nextcloud" {
		t.Fatalf("expected display_name untouched, got %q", card2.DisplayName)
	}
	if card2.RepoURL == nil || *card2.RepoURL != "https://example/repo" {
		t.Fatalf("expected repo_url updated, got %+v", card2.RepoURL)
	}
}

func TestUpsertProjectCard_MinimalDefaultsDisplayNameToSlug(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	card, _, err := s.UpsertProjectCard(UpsertProjectCardParams{Slug: "middleware"})
	if err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	if card.DisplayName != "middleware" {
		t.Fatalf("expected display_name fallback to slug, got %q", card.DisplayName)
	}
}

func TestGetProjectCard_NoCard(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	_, err := s.GetProjectCard("ghost")
	if !errors.Is(err, ErrNoProjectCard) {
		t.Fatalf("expected ErrNoProjectCard, got %v", err)
	}
}

func TestProjectCardCounts(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	if _, _, err := s.UpsertProjectCard(UpsertProjectCardParams{Slug: "nextcloud"}); err != nil {
		t.Fatalf("UpsertProjectCard: %v", err)
	}
	if err := s.CreateSession("s1", "nextcloud", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "manual", Title: "t", Content: "c", Project: "nextcloud"}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	result, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-1"), Title: strp("t"), Kind: strp("bugfix")})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	counts, err := s.ProjectCardCounts("nextcloud")
	if err != nil {
		t.Fatalf("ProjectCardCounts: %v", err)
	}
	if counts.Observations != 1 {
		t.Errorf("expected 1 observation, got %d", counts.Observations)
	}
	if counts.TasksTotal != 1 || counts.TasksActive != 1 {
		t.Errorf("expected 1 active task, got total=%d active=%d", counts.TasksTotal, counts.TasksActive)
	}
	_ = result
}

func TestUpsertTask_PrecedenceAndConflict(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	// Create via jira_key.
	r1, err := s.UpsertTask(UpsertTaskParams{
		Project: "nextcloud", JiraKey: strp("PROJ-100"), Title: strp("Fix previews"), Kind: strp("incident"),
	})
	if err != nil {
		t.Fatalf("UpsertTask create: %v", err)
	}
	if !r1.Created || !r1.CardCreated {
		t.Fatalf("expected created task + auto-created card, got %+v", r1)
	}

	// Update same task via jira_key, only branch changes.
	r2, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-100"), Branch: strp("fix/PROJ-100")})
	if err != nil {
		t.Fatalf("UpsertTask update: %v", err)
	}
	if r2.Created {
		t.Fatal("expected update, not create")
	}
	if r2.Task.ID != r1.Task.ID {
		t.Fatalf("expected same task id, got %d vs %d", r2.Task.ID, r1.Task.ID)
	}
	if r2.Task.Branch == nil || *r2.Task.Branch != "fix/PROJ-100" {
		t.Fatalf("expected branch updated, got %+v", r2.Task.Branch)
	}
	if r2.Task.Title != "Fix previews" {
		t.Fatalf("expected title untouched, got %q", r2.Task.Title)
	}

	// Same jira_key under a different project must conflict.
	_, err = s.UpsertTask(UpsertTaskParams{Project: "middleware", JiraKey: strp("PROJ-100"), Title: strp("x"), Kind: strp("bugfix")})
	var conflict *TaskKeyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected TaskKeyConflictError, got %v", err)
	}
	if conflict.ExistingProject != "nextcloud" {
		t.Fatalf("expected existing_project=nextcloud, got %q", conflict.ExistingProject)
	}

	// Missing title/kind on create.
	_, err = s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-200")})
	var missing *MissingFieldError
	if !errors.As(err, &missing) || missing.Field != "title" {
		t.Fatalf("expected MissingFieldError(title), got %v", err)
	}

	// sdd_change precedence for a brand new task.
	r3, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", SDDChange: strp("amx-upgrade"), Title: strp("Upgrade"), Kind: strp("feature")})
	if err != nil || !r3.Created {
		t.Fatalf("UpsertTask via sdd_change: created=%v err=%v", r3.Created, err)
	}
	r4, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", SDDChange: strp("amx-upgrade"), PRUrl: strp("https://pr")})
	if err != nil || r4.Created || r4.Task.ID != r3.Task.ID {
		t.Fatalf("expected update of same sdd_change task, got %+v err=%v", r4, err)
	}
}

func TestUpsertTask_JiraStatusDerivesStateAndClosedAt(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	r, err := s.UpsertTask(UpsertTaskParams{
		Project: "nextcloud", JiraKey: strp("PROJ-300"), Title: strp("t"), Kind: strp("incident"),
		JiraStatus: strp("In Develop"),
	})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if r.Task.State != "in_progress" {
		t.Fatalf("expected state derived to in_progress, got %q", r.Task.State)
	}
	if r.Task.StateSyncedAt == nil {
		t.Fatal("expected state_synced_at to be set")
	}

	r2, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-300"), JiraStatus: strp("Done")})
	if err != nil {
		t.Fatalf("UpsertTask transition to done: %v", err)
	}
	if r2.Task.State != "done" {
		t.Fatalf("expected state=done, got %q", r2.Task.State)
	}
	if r2.Task.ClosedAt == nil {
		t.Fatal("expected closed_at set when state becomes done")
	}

	r3, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-300"), JiraStatus: strp("Reopened")})
	if err != nil {
		t.Fatalf("UpsertTask reopen: %v", err)
	}
	if r3.Task.State != "open" {
		t.Fatalf("expected state=open after Reopened, got %q", r3.Task.State)
	}
	if r3.Task.ClosedAt != nil {
		t.Fatalf("expected closed_at cleared after reopen, got %v", *r3.Task.ClosedAt)
	}
}

func TestListTasks_FiltersAndStale(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	if _, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-1"), Title: strp("Active one"), Kind: strp("bugfix")}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if _, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-2"), Title: strp("Closed one"), Kind: strp("bugfix"), State: strp("done")}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	items, total, err := s.ListTasks("nextcloud", TaskListFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected default active filter to exclude done task, got total=%d items=%d", total, len(items))
	}
	if !items[0].StateStale {
		t.Fatal("expected state_stale=true when state_synced_at was never set")
	}

	itemsAll, totalAll, err := s.ListTasks("nextcloud", TaskListFilter{State: "done"})
	if err != nil {
		t.Fatalf("ListTasks(state=done): %v", err)
	}
	if totalAll != 1 || len(itemsAll) != 1 {
		t.Fatalf("expected 1 done task, got total=%d items=%d", totalAll, len(itemsAll))
	}

	byQuery, _, err := s.ListTasks("nextcloud", TaskListFilter{State: "active", Query: "Active"})
	if err != nil {
		t.Fatalf("ListTasks(query): %v", err)
	}
	if len(byQuery) != 1 || byQuery[0].JiraKey == nil || *byQuery[0].JiraKey != "PROJ-1" {
		t.Fatalf("expected FTS query to find PROJ-1, got %+v", byQuery)
	}
}

func TestResolveTaskRef_AllForms(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	r, err := s.UpsertTask(UpsertTaskParams{
		Project: "nextcloud", JiraKey: strp("PROJ-42"), SDDChange: strp("proj-42"), Title: strp("t"), Kind: strp("incident"),
	})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	byJira, err := s.ResolveTaskRef("nextcloud", "PROJ-42")
	if err != nil || byJira.ID != r.Task.ID {
		t.Fatalf("resolve by jira_key failed: %+v err=%v", byJira, err)
	}
	bySync, err := s.ResolveTaskRef("nextcloud", r.Task.SyncID)
	if err != nil || bySync.ID != r.Task.ID {
		t.Fatalf("resolve by sync_id failed: %+v err=%v", bySync, err)
	}
	byID, err := s.ResolveTaskRef("nextcloud", "#"+itoa(r.Task.ID))
	if err != nil || byID.ID != r.Task.ID {
		t.Fatalf("resolve by #id failed: %+v err=%v", byID, err)
	}
	byChange, err := s.ResolveTaskRef("nextcloud", "change:proj-42")
	if err != nil || byChange.ID != r.Task.ID {
		t.Fatalf("resolve by change: failed: %+v err=%v", byChange, err)
	}
	if _, err := s.ResolveTaskRef("nextcloud", "PROJ-999"); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("expected ErrUnknownTask, got %v", err)
	}
	// Scoped to project: the same #id under another project must not resolve.
	if _, err := s.ResolveTaskRef("middleware", "#"+itoa(r.Task.ID)); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("expected ErrUnknownTask across projects, got %v", err)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestLinkTaskObservation_CrossProjectRejected(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	r, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-1"), Title: strp("t"), Kind: strp("bugfix")})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := s.CreateSession("s1", "middleware", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "manual", Title: "t", Content: "c", Project: "middleware"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	_, err = s.LinkTaskObservation(LinkTaskObservationParams{Task: r.Task, ObservationID: obsID})
	if !errors.Is(err, ErrCrossProjectLink) {
		t.Fatalf("expected ErrCrossProjectLink, got %v", err)
	}
}

func TestLinkTaskObservation_RefsAndRoleDefault(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	r, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-1"), Title: strp("t"), Kind: strp("incident")})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := s.CreateSession("s1", "nextcloud", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "bugfix", Title: "root cause", Content: "c", Project: "nextcloud", TopicKey: "incident/PROJ-1",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	commit := "3f9c2a7d1b8e4c6f0a2d9e1b7c5f3a8d2e6b4c1a"
	result, err := s.LinkTaskObservation(LinkTaskObservationParams{
		Task: r.Task, ObservationID: obsID,
		KnowledgeRef: strp("Services/Nextcloud/Architecture.md"),
		GraphRef:     strp("ObjectStoreStorage::writeStream"),
		GraphCommit:  &commit,
	})
	if err != nil {
		t.Fatalf("LinkTaskObservation: %v", err)
	}
	if !result.Linked {
		t.Fatal("expected linked=true on first link")
	}
	if result.Role != "root_cause" {
		t.Fatalf("expected role defaulted to root_cause from topic_key, got %q", result.Role)
	}
	if result.RefsAdded != 2 {
		t.Fatalf("expected 2 refs added, got %d (%+v)", result.RefsAdded, result.Refs)
	}

	// graph_ref without graph_commit must fail.
	obsID2, err := s.AddObservation(AddObservationParams{SessionID: "s1", Type: "manual", Title: "t2", Content: "c", Project: "nextcloud"})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	_, err = s.LinkTaskObservation(LinkTaskObservationParams{Task: r.Task, ObservationID: obsID2, GraphRef: strp("Foo::bar")})
	if !errors.Is(err, ErrGraphCommitRequired) {
		t.Fatalf("expected ErrGraphCommitRequired, got %v", err)
	}

	// Re-linking the same pair reports linked=false.
	result2, err := s.LinkTaskObservation(LinkTaskObservationParams{Task: r.Task, ObservationID: obsID})
	if err != nil {
		t.Fatalf("LinkTaskObservation (again): %v", err)
	}
	if result2.Linked {
		t.Fatal("expected linked=false when the link already existed")
	}
}

func TestAddEvidence_DuplicateAndLimits(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	r, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-1"), Title: strp("t"), Kind: strp("incident")})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	sha := "9f2b1c0a7e4d5b6c8a1f3e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0e1d2c3b"
	oversized := int64(3_000_000)
	ev, dup, limits, err := s.AddEvidence(AddEvidenceParams{
		Task: r.Task, Path: "acme/PROJ-1/shot.png", SHA256: sha, Kind: "png", Proves: "it works",
		SizeBytes: &oversized,
	})
	if err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}
	if dup {
		t.Fatal("expected duplicate=false on first insert")
	}
	if limits.OK {
		t.Fatalf("expected png over 2097152 bytes to violate the limit, got %+v", limits)
	}
	if ev.SyncID == "" {
		t.Fatal("expected sync_id assigned")
	}

	ev2, dup2, _, err := s.AddEvidence(AddEvidenceParams{
		Task: r.Task, Path: "acme/PROJ-1/shot.png", SHA256: sha, Kind: "png", Proves: "it works (retry)",
	})
	if err != nil {
		t.Fatalf("AddEvidence (duplicate): %v", err)
	}
	if !dup2 {
		t.Fatal("expected duplicate=true on the second insert with the same (task, sha256)")
	}
	if ev2.ID != ev.ID {
		t.Fatalf("expected duplicate to return the original row, got %d vs %d", ev2.ID, ev.ID)
	}
}

func TestListEvidence(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	r, err := s.UpsertTask(UpsertTaskParams{Project: "nextcloud", JiraKey: strp("PROJ-1"), Title: strp("t"), Kind: strp("incident")})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	size := int64(1000)
	if _, _, _, err := s.AddEvidence(AddEvidenceParams{
		Task: r.Task, Path: "a.png", SHA256: "9f2b1c0a7e4d5b6c8a1f3e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0e1d2c3b",
		Kind: "png", Proves: "p1", SizeBytes: &size, AttachedJira: true,
	}); err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}

	items, total, totalBytes, err := s.ListEvidence("nextcloud", EvidenceListFilter{TaskSyncID: r.Task.SyncID})
	if err != nil {
		t.Fatalf("ListEvidence: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected 1 evidence item, got total=%d items=%d", total, len(items))
	}
	if totalBytes != 1000 {
		t.Fatalf("expected total_bytes=1000, got %d", totalBytes)
	}
	if items[0].JiraKey == nil || *items[0].JiraKey != "PROJ-1" {
		t.Fatalf("expected jira_key joined from task, got %+v", items[0].JiraKey)
	}
}

func TestSyncRunbookIndex_SkipsTemplatesAndInvalidStatus(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	result, err := s.SyncRunbookIndex(RunbookIndexSyncParams{
		Source: "knowledge-mcp",
		Entries: []RunbookIndexEntryInput{
			{
				ID: "RB-003", VaultPath: "Runbooks/Performance/RB-003.md", Title: "Preview slow", Service: "nextcloud",
				Category: "performance", Status: "verified", Symptoms: []string{"503 on preview"}, NeedsReview: boolp(true),
			},
			{
				ID: "RB-000", VaultPath: "Runbooks/Templates/Auth Issue Template.md", Title: "tpl", Service: "middleware",
				Category: "auth", Status: "draft", Tags: []string{"template"},
			},
			{
				ID: "RB-004", VaultPath: "Runbooks/Foo.md", Title: "bad status", Service: "nextcloud",
				Category: "auth", Status: "open",
			},
		},
	})
	if err != nil {
		t.Fatalf("SyncRunbookIndex: %v", err)
	}
	if result.Upserted != 1 {
		t.Fatalf("expected 1 upserted, got %d", result.Upserted)
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("expected 2 skipped entries, got %+v", result.Skipped)
	}
	if result.StaleCount != 1 {
		t.Fatalf("expected stale_count=1 (needs_review), got %d", result.StaleCount)
	}

	reasons := map[string]string{}
	for _, sk := range result.Skipped {
		reasons[sk.ID] = sk.Reason
	}
	if reasons["RB-000"] != "template" {
		t.Errorf("expected RB-000 skipped as template, got %q", reasons["RB-000"])
	}
	if reasons["RB-004"] != "invalid_status" {
		t.Errorf("expected RB-004 skipped as invalid_status, got %q", reasons["RB-004"])
	}

	// Re-sending the identical valid entry reports unchanged, not upserted.
	result2, err := s.SyncRunbookIndex(RunbookIndexSyncParams{
		Source: "knowledge-mcp",
		Entries: []RunbookIndexEntryInput{
			{
				ID: "RB-003", VaultPath: "Runbooks/Performance/RB-003.md", Title: "Preview slow", Service: "nextcloud",
				Category: "performance", Status: "verified", Symptoms: []string{"503 on preview"}, NeedsReview: boolp(true),
			},
		},
	})
	if err != nil {
		t.Fatalf("SyncRunbookIndex (resend): %v", err)
	}
	if result2.Unchanged != 1 || result2.Upserted != 0 {
		t.Fatalf("expected unchanged=1 upserted=0 on resend, got %+v", result2)
	}
}

func TestSyncRunbookIndex_PruneMissing(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	if _, err := s.SyncRunbookIndex(RunbookIndexSyncParams{
		Source: "vault-fs",
		Entries: []RunbookIndexEntryInput{
			{ID: "RB-001", VaultPath: "Runbooks/RB-001.md", Title: "one", Service: "nextcloud", Category: "auth", Status: "verified"},
			{ID: "RB-002", VaultPath: "Runbooks/RB-002.md", Title: "two", Service: "nextcloud", Category: "auth", Status: "verified"},
		},
	}); err != nil {
		t.Fatalf("seed SyncRunbookIndex: %v", err)
	}

	result, err := s.SyncRunbookIndex(RunbookIndexSyncParams{
		Project:      "nextcloud",
		Source:       "vault-fs",
		PruneMissing: true,
		Entries: []RunbookIndexEntryInput{
			{ID: "RB-001", VaultPath: "Runbooks/RB-001.md", Title: "one", Service: "nextcloud", Category: "auth", Status: "verified"},
		},
	})
	if err != nil {
		t.Fatalf("SyncRunbookIndex (prune): %v", err)
	}
	if result.Pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d", result.Pruned)
	}

	items, _, err := s.FindRunbooks(RunbookFindParams{Query: "two", Project: "nextcloud"})
	if err != nil {
		t.Fatalf("FindRunbooks: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected pruned runbook RB-002 to no longer be findable, got %+v", items)
	}
}

func TestFindRunbooks(t *testing.T) {
	s := newProjectsSchemaTestStore(t)
	if _, err := s.SyncRunbookIndex(RunbookIndexSyncParams{
		Source: "knowledge-mcp",
		Entries: []RunbookIndexEntryInput{
			{
				ID: "RB-003", VaultPath: "Runbooks/RB-003.md", Title: "Preview endpoint slow or failing", Service: "nextcloud",
				Category: "performance", Pattern: "missing-files", Severity: "P2", Status: "verified",
				Symptoms: []string{"GET /index.php/core/preview returns 503", "previews time out"},
			},
		},
	}); err != nil {
		t.Fatalf("SyncRunbookIndex: %v", err)
	}

	items, total, err := s.FindRunbooks(RunbookFindParams{Query: "preview 503 object store", Project: "nextcloud"})
	if err != nil {
		t.Fatalf("FindRunbooks: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "RB-003" {
		t.Fatalf("expected RB-003 to match, got total=%d items=%+v", total, items)
	}
}

func boolp(v bool) *bool { return &v }

func TestDefaultJiraProject(t *testing.T) {
	// Same reasoning as the Jira base URL: the fallback key must not name a
	// particular organisation's project.
	t.Setenv("ENGRAM_JIRA_PROJECT", "")
	if got := DefaultJiraProject(); got != "PROJ" {
		t.Fatalf("unset: expected the generic default, got %q", got)
	}

	t.Setenv("ENGRAM_JIRA_PROJECT", "ACME")
	if got := DefaultJiraProject(); got != "ACME" {
		t.Fatalf("set: expected the configured key, got %q", got)
	}

	t.Setenv("ENGRAM_JIRA_PROJECT", "  ")
	if got := DefaultJiraProject(); got != "PROJ" {
		t.Fatalf("blank: expected the default, got %q", got)
	}
}
