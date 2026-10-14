package main

import (
	"strconv"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCaptureProjectProvenanceCLISave(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	withCwd(t, t.TempDir())
	withArgs(t, "engram", "save", "Evidence title", "Evidence content", "--project", "example")
	stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })
	if stdout != "Memory saved: #1 \"Evidence title\" (manual)\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	st, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	observations, err := st.RecentObservations("example", "project", 10)
	if err != nil || len(observations) != 1 {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	for _, identity := range [][2]string{
		{"observation", strconv.FormatInt(observations[0].ID, 10)},
		{"session", observations[0].SessionID},
	} {
		p, err := st.CaptureProjectProvenance(identity[0], identity[1])
		if err != nil || p == nil || p.Source != "explicit_override" || p.RawProject != "example" {
			t.Fatalf("%v evidence=%+v err=%v", identity, p, err)
		}
	}
}
