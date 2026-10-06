package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func postSession(t *testing.T, st *store.Store, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(body)))
	return rec
}

// A blank-directory shared row is never adopted by an isolated
// registration: the root and a selected continuation both return 409
// session_isolation_conflict with the stored identity, lease and journal intact.
func TestIsolatedRegistrationRejectsSharedIdentity(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		t.Run(fmt.Sprintf("continuation=%t", continuation), func(t *testing.T) {
			t.Parallel()
			st := newServerTestStore(t)
			if err := st.EnrollProject("target"); err != nil {
				t.Fatal(err)
			}
			selected := "satellite"
			if continuation {
				if rec := postSession(t, st, `{"id":"satellite","project":"target","ownership_mode":"project_owned","isolated":true,"resume":true}`); rec.Code != http.StatusCreated {
					t.Fatalf("isolated root: %d %s", rec.Code, rec.Body.String())
				}
				if err := st.EndSession("satellite", "terminal"); err != nil {
					t.Fatal(err)
				}
				selected = "satellite:resume:2"
			}
			// HTTP runtime registration always resolves a directory; a blank shared
			// row comes from store-level registration or import.
			if err := st.StartSession(selected, "target", ""); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetSession(selected)
			if err != nil || before.OwnershipMode != store.SessionOwnershipShared {
				t.Fatalf("shared fixture = %#v, %v", before, err)
			}
			journalBefore, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
			if err != nil {
				t.Fatal(err)
			}
			// Lease renewal has one-second resolution; let a renewal become observable.
			time.Sleep(1100 * time.Millisecond)

			rec := postSession(t, st, `{"id":"satellite","project":"target","ownership_mode":"project_owned","isolated":true,"resume":true}`)
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"session_isolation_conflict"`) {
				t.Fatalf("expected 409 session_isolation_conflict, got %d: %s", rec.Code, rec.Body.String())
			}
			after, err := st.GetSession(selected)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejection mutated session or lease: %#v -> %#v", before, after)
			}
			journalAfter, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(journalBefore, journalAfter) {
				t.Fatalf("rejection changed the sync journal: %d -> %d rows", len(journalBefore), len(journalAfter))
			}
			if _, err := st.GetSession("satellite:resume:3"); err == nil {
				t.Fatal("rejection created a continuation")
			}
		})
	}
}
