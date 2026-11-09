package store

import "testing"

func TestLocalSessionProvenance(t *testing.T) {
	s := newTestStore(t)
	check := func(id, owner string, eligible bool) {
		t.Helper()
		gotOwner, gotEligible, err := s.LocalSessionProvenance(id)
		if err != nil || gotOwner != owner || gotEligible != eligible {
			t.Fatalf("%q: owner=%q eligible=%v err=%v", id, gotOwner, gotEligible, err)
		}
	}
	if err := s.CreateSession("new", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	check("new", "alpha", true)
	if _, err := s.DB().Exec(`UPDATE sessions SET project='beta' WHERE id='new'`); err != nil {
		t.Fatal(err)
	}
	check("new", "beta", false)
	if _, err := s.DB().Exec(`UPDATE sessions SET project='alpha' WHERE id='new'`); err != nil {
		t.Fatal(err)
	}
	check("new", "alpha", true)
	if err := s.StartSession("running", "alpha", "/work"); err != nil {
		t.Fatal(err)
	}
	check("running", "alpha", true)
	if _, err := s.DB().Exec(`INSERT INTO sessions(id,project,directory) VALUES ('imported','alpha','/work'),('pulled','alpha','/work')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(&ExportData{Sessions: []Session{{ID: "backup", Project: "alpha", Directory: "/work", StartedAt: Now()}}}); err != nil {
		t.Fatal(err)
	}
	check("backup", "alpha", false)
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{
		Seq: 1, Entity: SyncEntitySession, EntityKey: "remote", Op: SyncOpUpsert,
		Payload: `{"id":"remote","project":"alpha","directory":"/work","started_at":"2025-01-01 00:00:00"}`,
		Source:  SyncSourceRemote, Project: "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	check("remote", "alpha", false)
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{
		Seq: 2, Entity: SyncEntitySession, EntityKey: "new", Op: SyncOpUpsert,
		Payload: `{"id":"new","project":"alpha","directory":"/work","started_at":"2025-01-01 00:00:00"}`,
		Source:  SyncSourceRemote, Project: "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	check("new", "alpha", true)
	for _, id := range []string{"imported", "pulled", "backup", "remote"} {
		check(id, "alpha", false)
		if err := s.CreateSession(id, "alpha", "/work"); err != nil {
			t.Fatal(err)
		}
		if err := s.StartSession(id, "alpha", "/work"); err != nil {
			t.Fatal(err)
		}
		check(id, "alpha", false)
	}
	check("missing", "", false)
}

func TestLocalSessionProvenanceMigration(t *testing.T) {
	cfg := FallbackConfig(t.TempDir())
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sessions(id,project,directory) VALUES ('legacy','alpha','/work')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`ALTER TABLE sessions DROP COLUMN local_creation_project`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LocalSessionProvenance("legacy"); err == nil {
		t.Fatal("missing provenance column did not fail closed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	owner, eligible, err := s.LocalSessionProvenance("legacy")
	if err != nil || owner != "alpha" || eligible {
		t.Fatalf("migration: %q %v %v", owner, eligible, err)
	}
}

func TestLocalSessionProvenanceRepairRejectsChangedOrigin(t *testing.T) {
	s := newTestStore(t)
	seedSessionIdentityRepair(t, s, false)
	plan, err := s.PlanSessionIdentityRepair("", "repaired")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE sessions SET local_creation_project='alpha' WHERE id=''`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySessionIdentityRepair(plan); err == nil {
		t.Fatal("repair accepted stale creation-owner evidence")
	}
	owner, eligible, err := s.LocalSessionProvenance("repaired")
	if err != nil || owner != "" || eligible {
		t.Fatalf("stale repair inserted replacement: %q %v %v", owner, eligible, err)
	}
}

func TestLocalSessionProvenanceRepair(t *testing.T) {
	for _, tc := range []struct {
		name            string
		creationProject any
		eligible        bool
	}{{"unknown", nil, false}, {"local", "alpha", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedSessionIdentityRepair(t, s, false)
			if _, err := s.DB().Exec(`UPDATE sessions SET local_creation_project=? WHERE id=''`, tc.creationProject); err != nil {
				t.Fatal(err)
			}
			plan, err := s.PlanSessionIdentityRepair("", "repaired")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ApplySessionIdentityRepair(plan); err != nil {
				t.Fatal(err)
			}
			owner, eligible, err := s.LocalSessionProvenance("repaired")
			if err != nil || owner != "alpha" || eligible != tc.eligible {
				t.Fatalf("repair: %q %v %v", owner, eligible, err)
			}
			if _, err := s.DB().Exec(`UPDATE sessions SET project='beta' WHERE id='repaired'`); err != nil {
				t.Fatal(err)
			}
			owner, eligible, err = s.LocalSessionProvenance("repaired")
			if err != nil || owner != "beta" || eligible {
				t.Fatalf("changed repaired owner: %q %v %v", owner, eligible, err)
			}
		})
	}
}
