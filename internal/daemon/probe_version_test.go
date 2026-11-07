package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeVersionMetadataPreservesToken(t *testing.T) {
	for _, tc := range []struct {
		name, body, version, token string
		status                     int
	}{
		{"release", `{"service":"engram","version":"3.0.0","daemon_token":"ours"}`, "3.0.0", "ours", 200},
		{"legacy", `{"service":"engram","daemon_token":"ours"}`, "", "ours", 200},
		{"number", `{"service":"engram","version":123,"daemon_token":"ours"}`, "", "ours", 200},
		{"null", `{"service":"engram","version":null,"daemon_token":"ours"}`, "", "ours", 200},
		{"array", `{"service":"engram","version":[],"daemon_token":"ours"}`, "", "ours", 200},
		{"dev", `{"service":"engram","version":"dev","daemon_token":"ours"}`, "dev", "ours", 200},
		{"foreign", `{"service":"other","version":"3.0.0","daemon_token":"ours"}`, "", "ours", 200},
		{"missing service", `{"version":"3.0.0","daemon_token":"ours"}`, "", "ours", 200},
		{"malformed", `not-json`, "", "", 200},
		{"non success", `{"service":"engram","version":"3.0.0","daemon_token":"ours"}`, "", "", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); _, _ = fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			res := Probe(context.Background(), srv.Listener.Addr().(*net.TCPAddr).Port, time.Second)
			want := StatusRunning
			if tc.status != 200 {
				want = StatusUnreachable
			}
			if res.Status != want || res.Version != tc.version || res.Token != tc.token {
				t.Fatalf("probe: %+v", res)
			}
		})
	}
}
