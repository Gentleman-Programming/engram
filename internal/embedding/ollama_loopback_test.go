package embedding

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoopbackDialFallback(t *testing.T) {
	ips := []net.IPAddr{{IP: net.ParseIP("::1")}, {IP: net.ParseIP("127.0.0.1")}}
	lookup := func(context.Context, string) ([]net.IPAddr, error) { return ips, nil }
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	refused := errors.New("controlled connection refusal")
	var attempts []string
	var deadlines []time.Time
	conn, err := dialLoopback(context.Background(), "tcp", "localhost:11434", lookup,
		func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Errorf("network = %q", network)
			}
			attempts = append(attempts, address)
			deadline, _ := ctx.Deadline()
			deadlines = append(deadlines, deadline)
			if len(attempts) == 1 {
				return nil, refused
			}
			return client, nil
		})
	if err != nil || conn != client || !reflect.DeepEqual(attempts, []string{"[::1]:11434", "127.0.0.1:11434"}) {
		t.Fatalf("loopback fallback: conn=%v err=%v attempts=%v", conn, err, attempts)
	}
	if deadlines[0].IsZero() || !deadlines[0].Equal(deadlines[1]) || time.Until(deadlines[0]) > 10*time.Second {
		t.Fatalf("attempts must share one bounded connection budget: %v", deadlines)
	}
}

func TestLoopbackDialValidatesWholeResolutionBeforeConnecting(t *testing.T) {
	for _, ips := range [][]net.IPAddr{nil, {{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("192.0.2.1")}}} {
		attempts := 0
		conn, err := dialLoopback(context.Background(), "tcp", "localhost:11434",
			func(context.Context, string) ([]net.IPAddr, error) { return ips, nil },
			func(context.Context, string, string) (net.Conn, error) {
				attempts++
				return nil, errors.New("must not connect")
			})
		if conn != nil || !errors.Is(err, ErrConfig) || attempts != 0 {
			t.Fatalf("untrusted resolution reached dial: conn=%v err=%v attempts=%d", conn, err, attempts)
		}
	}
}

func TestLoopbackDialFailureAndCancellation(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		refused := errors.New("controlled refusal")
		attempts := 0
		conn, err := dialLoopback(ctx, "tcp", "localhost:11434",
			func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("::1")}, {IP: net.ParseIP("127.0.0.1")}}, nil
			},
			func(context.Context, string, string) (net.Conn, error) {
				attempts++
				if cancelFirst {
					cancel()
				}
				return nil, refused
			})
		cancel()
		wantErr, wantAttempts := refused, 2
		if cancelFirst {
			wantErr, wantAttempts = context.Canceled, 1
		}
		if conn != nil || !errors.Is(err, wantErr) || attempts != wantAttempts {
			t.Fatalf("cancel=%v: conn=%v err=%v attempts=%d", cancelFirst, conn, err, attempts)
		}
	}
}

func TestResolveLocalhostIPv4Listener(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(tags(identity, "")))
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: strings.Replace(server.URL, "127.0.0.1", "localhost", 1), Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := c.Resolve(ctx)
	if err != nil || got != identity {
		t.Fatalf("localhost must reach a numeric loopback listener: %+v %v", got, err)
	}
}
