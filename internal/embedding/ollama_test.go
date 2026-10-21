package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConfiguration(t *testing.T) {
	for _, base := range []string{"https://127.0.0.1", "http://192.0.2.1", "http://localhost/path", "http://u:p@localhost", "http://localhost?x", "http://localhost#x", "http://localhost:0", "http://localhost:65536", "http://localhost:"} {
		if _, err := New(Config{Model: "test", BaseURL: base}); err == nil {
			t.Errorf("accepted invalid URL %s", base)
		}
	}
	if _, err := New(Config{}); err == nil {
		t.Error("accepted missing model")
	}
	for _, base := range []string{"", "http://localhost:11434", "http://127.1.2.3:1234", "http://[::1]:11434"} {
		if _, err := New(Config{Model: "test", BaseURL: base}); err != nil {
			t.Errorf("valid URL: %v", err)
		}
	}
}

var identity = Identity{Model: "test:latest", Digest: strings.Repeat("a", 64)}

func tags(id Identity, remote string) string {
	return fmt.Sprintf(`{"models":[{"name":%q,"model":%q,"digest":%q,"remote_model":%q}]}`, id.Model, id.Model, id.Digest, remote)
}
func fixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{BaseURL: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.http.CloseIdleConnections)
	return c
}
func TestResolve(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"local", tags(identity, ""), 200, true},
		{"remote", tags(identity, "cloud"), 200, false},
		{"remoteHost", `{"models":[{"name":"test:latest","model":"test:latest","digest":"` + identity.Digest + `","remote_host":"https://cloud"}]}`, 200, false},
		{"missing", `{"models":[]}`, 200, false},
		{"digest", tags(Identity{"test:latest", "bad"}, ""), 200, false},
		{"ambiguous", `{"models":[{"name":"test:latest","digest":"` + identity.Digest + `"},{"name":"test:latest","digest":"` + identity.Digest + `"}]}`, 200, false},
		{"invalidCanonical", tags(Identity{"test:latest?invalid", identity.Digest}, ""), 200, false},
		{"inconsistentName", `{"models":[{"name":"test:latest","model":"other","digest":"` + identity.Digest + `"}]}`, 200, false},
		{"malformed", `{`, 200, false}, {"trailing", tags(identity, "") + ` {}`, 200, false},
		{"status", `{"error":"synthetic secret"}`, 500, false},
		{"oversize", strings.Repeat(" ", 4*1024*1024+1), 200, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/api/tags" {
					t.Error("unexpected operation")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			if calls != 0 {
				t.Fatal("constructor IO")
			}
			got, err := c.Resolve(context.Background())
			if tc.valid {
				if err != nil || got != identity {
					t.Fatalf("got %+v, %v", got, err)
				}
			} else if err == nil {
				t.Error("accepted invalid metadata")
			} else if strings.Contains(err.Error(), "synthetic secret") {
				t.Error("leaked body")
			}
			if calls != 1 {
				t.Errorf("calls=%d", calls)
			}
		})
	}
}

func TestEmbed(t *testing.T) {
	cases := []struct {
		name, body string
		change     int
		expected   Identity
		valid      bool
	}{
		{"twoDimensions", `{"model":"test:latest","embeddings":[[1,2],[3,4]]}`, 0, identity, true},
		{"threeDimensions", `{"model":"test:latest","embeddings":[[1,2,3],[4,5,6]]}`, 0, identity, true},
		{"zero", `{"model":"test:latest","embeddings":[[0,0],[1,2]]}`, 0, identity, false},
		{"empty", `{"model":"test:latest","embeddings":[[],[]]}`, 0, identity, false},
		{"count", `{"model":"test:latest","embeddings":[[1,2]]}`, 0, identity, false},
		{"dimension", `{"model":"test:latest","embeddings":[[1,2],[1]]}`, 0, identity, false},
		{"overflow", `{"model":"test:latest","embeddings":[[1e999,2],[1,2]]}`, 0, identity, false},
		{"normOverflow", `{"model":"test:latest","embeddings":[[1.7e308,1.7e308],[1,2]]}`, 0, identity, false},
		{"null", `{"model":"test:latest","embeddings":[[null,2],[1,2]]}`, 0, identity, false},
		{"wrongModel", `{"model":"other","embeddings":[[1,2],[1,2]]}`, 0, identity, false},
		{"mixedError", `{"model":"test:latest","embeddings":[[1,2],[1,2]],"error":"synthetic secret"}`, 0, identity, false},
		{"errorPayload", `{"error":"synthetic secret"}`, 0, identity, false},
		{"trailing", `{"model":"test:latest","embeddings":[[1,2],[1,2]]} {}`, 0, identity, false},
		{"beforeChange", `{"model":"test:latest","embeddings":[[1,2],[1,2]]}`, 1, identity, false},
		{"afterChange", `{"model":"test:latest","embeddings":[[1,2],[1,2]]}`, 2, identity, false},
		{"invalidExpected", `{}`, 0, Identity{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reads, posts := 0, 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/tags":
					reads++
					id := identity
					if tc.change > 0 && reads >= tc.change {
						id.Digest = strings.Repeat("b", 64)
					}
					fmt.Fprint(w, tags(id, ""))
				case "/api/embed":
					posts++
					if r.Method != "POST" {
						t.Error("not POST")
					}
					var payload struct {
						Model    string
						Input    []string
						Truncate *bool
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload.Model != identity.Model || len(payload.Input) != 2 || payload.Input[0] != "synthetic one" || payload.Truncate == nil || *payload.Truncate {
						t.Errorf("wrong payload: %+v", payload)
					}
					fmt.Fprint(w, tc.body)
				default:
					t.Error("hidden operation")
					w.WriteHeader(500)
				}
			})
			got, err := c.Embed(context.Background(), tc.expected, []string{"synthetic one", "synthetic two"})
			if tc.valid {
				if err != nil || len(got) != 2 {
					t.Fatalf("got %v, %v", got, err)
				}
			} else if err == nil || got != nil {
				t.Errorf("accepted invalid result: %v %v", got, err)
			}
			if tc.name == "beforeChange" && posts != 0 {
				t.Error("embedded after identity mismatch")
			}
			if tc.name == "afterChange" && reads != 2 {
				t.Error("missing postcheck")
			}
			if tc.name == "invalidExpected" && reads+posts != 0 {
				t.Error("IO for invalid expected identity")
			}
			if posts > 1 {
				t.Error("retried")
			}
		})
	}
}
func TestEmptyAndCancellation(t *testing.T) {
	calls := 0
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, tags(identity, "")) })
	if _, err := c.Embed(context.Background(), identity, nil); err == nil {
		t.Error("accepted empty input")
	}
	if calls != 0 {
		t.Error("empty input made request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Resolve(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("lost cancellation: %v", err)
	}
}
func TestInvalidCanonicalIdentity(t *testing.T) {
	for _, model := range []string{"test", "test:", "test:latest?invalid", "test:latest#invalid", "test:latest@invalid"} {
		t.Run(model, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tags(Identity{model, identity.Digest}, ""))
			})
			c.model = model
			if _, err := c.Resolve(context.Background()); err == nil {
				t.Error("accepted malformed canonical name")
			}
		})
	}
}

func TestActionableFailures(t *testing.T) {
	t.Run("offline", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		c, err := New(Config{BaseURL: server.URL, Model: "test"})
		if err != nil {
			t.Fatal(err)
		}
		server.Close()
		if _, err := c.Resolve(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("offline: %v", err)
		}
	})
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"missing", `{"models":[]}`, 200, ErrModel},
		{"cloud", tags(identity, "cloud"), 200, ErrRemoteModel},
		{"status", `{"error":"synthetic secret"}`, 503, ErrUnavailable},
		{"badJSON", `{`, 200, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			if _, err := c.Resolve(context.Background()); !errors.Is(err, tc.want) {
				t.Errorf("classification: %v", err)
			}
		})
	}
	t.Run("embedStatus", func(t *testing.T) {
		calls := 0
		c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/tags" {
				fmt.Fprint(w, tags(identity, ""))
				return
			}
			calls++
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"synthetic secret"}`)
		})
		if result, err := c.Embed(context.Background(), identity, []string{"synthetic"}); result != nil || !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "synthetic secret") {
			t.Errorf("invalid status result: %v %v", result, err)
		}
		if calls != 1 {
			t.Errorf("requests: %d", calls)
		}
	})
}

func TestBodyDeadline(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	c.http.Timeout = 20 * time.Millisecond
	if _, err := c.Resolve(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("lost body deadline: %v", err)
	}
}

func TestDeadlineAndTransport(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Resolve(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("lost deadline: %v", err)
	}
	transport := c.http.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Error("proxy enabled")
	}
	if !transport.DisableKeepAlives {
		t.Error("reused connections can trigger implicit transport retries")
	}
	if c.http.Timeout != 120*time.Second {
		t.Error("missing bounded timeout")
	}
}

func TestRedirect(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	if _, err := c.Resolve(context.Background()); err == nil {
		t.Error("accepted redirect")
	}
	if targetCalls != 0 {
		t.Error("followed redirect")
	}
}
