package store

import "testing"

// Keep the same store open throughout: reopening can conceal the rescue bug
// by running the legacy ownership migration.
func TestRescueBlankProjectClassifiesOwnershipWithoutReopening(t *testing.T) {
	for _, tc := range []struct {
		name, id, project, wantMode string
		mode                        any
	}{
		{"shared NULL", "legacy-session", "<NULL>", SessionOwnershipShared, nil},
		{"manual NULL", "manual-save-target", "<NULL>", SessionOwnershipProjectOwned, nil},
		{"shared blank", "blank-session", "", SessionOwnershipShared, ""},
		{"manual whitespace", "manual-save-target", " \t", SessionOwnershipProjectOwned, " \t"},
		{"preserve shared", "manual-save-target", "<NULL>", SessionOwnershipShared, SessionOwnershipShared},
		{"preserve project owned", "legacy-session", "<NULL>", SessionOwnershipProjectOwned, SessionOwnershipProjectOwned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type legacySession struct{ id, project string }
			s := newTestStoreWithNullableLegacySessions(t, legacySession{tc.id, tc.project})
			if _, err := s.DB().Exec(`UPDATE sessions SET ownership_mode = ? WHERE id = ?`, tc.mode, tc.id); err != nil {
				t.Fatalf("seed legacy ownership mode: %v", err)
			}
			params := ProjectRescueParams{TargetProject: "target", SessionIDs: []string{tc.id}}
			result, err := s.RescueNullProjectOwnership(params)
			if err != nil {
				t.Fatalf("RescueNullProjectOwnership: %v", err)
			}
			if result.RescuedSessions != 1 || result.Rescued() != 1 || !result.Complete {
				t.Errorf("first rescue = %#v, want one complete rescued session", result)
			}
			session, err := s.GetSession(tc.id)
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if session.Project != "target" || session.OwnershipMode != tc.wantMode {
				t.Errorf("session = %#v, want project target and ownership mode %q", session, tc.wantMode)
			}
			again, err := s.RescueNullProjectOwnership(params)
			if err != nil {
				t.Fatalf("repeat rescue: %v", err)
			}
			if again.RescuedSessions != 0 || again.Rescued() != 0 || again.SkippedRecords != 1 || !again.Complete {
				t.Errorf("repeat rescue = %#v, want no rescue and one skipped session", again)
			}
		})
	}
}
