package cloudserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudRootRedirect(t *testing.T) {
	srv := New(&fakeStore{}, strictBearerAuth{token: "test-token"}, 0)
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	for _, tc := range []struct {
		path     string
		status   int
		location string
		body     string
	}{
		{path: "/", status: http.StatusFound, location: "/dashboard/", body: "<a href=\"/dashboard/\">Found</a>.\n\n"},
		{path: "/unknown", status: http.StatusNotFound, body: "404 page not found\n"},
		{path: "/health", status: http.StatusOK, body: "{\"service\":\"engram-cloud\",\"status\":\"ok\"}\n"},
		{path: "/dashboard/", status: http.StatusSeeOther, location: "/dashboard/login?next=%2Fdashboard"},
		{path: "/dashboard/login", status: http.StatusOK},
		{path: "/sync/mutations/pull", status: http.StatusUnauthorized, body: "unauthorized: missing authorization header\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := client.Get(server.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Errorf("close response body: %v", err)
				}
			}()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Errorf("status = %d, want %d; body=%q", resp.StatusCode, tc.status, body)
			}
			if got := resp.Header.Get("Location"); got != tc.location {
				t.Errorf("Location = %q, want %q", got, tc.location)
			}
			if tc.body != "" && string(body) != tc.body {
				t.Errorf("body = %q, want %q", body, tc.body)
			}
		})
	}
}
