package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Eligibility describes the original lookup only; readiness is never an excuse
// to recover from a received HTTP response or a redirected request.
func hookLocalRecoveryEligible(platform, base, endpoint string, err error) bool {
	if platform != "windows" || strings.TrimSpace(os.Getenv("ENGRAM_URL")) != "" || strings.TrimSpace(os.Getenv("ENGRAM_SOCKET")) != "" || base != codexHookURL() {
		return false
	}
	u, parseErr := url.Parse(base)
	if parseErr != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	port, parseErr := strconv.Atoi(u.Port())
	if parseErr != nil || port < 1 || port > 65535 {
		return false
	}
	var denial *hookDenialError
	if errors.As(err, &denial) && denial.redirected {
		return false
	}
	var transport *url.Error
	var operation *net.OpError
	if !errors.As(err, &transport) || transport.Op != "Get" || transport.URL != endpoint ||
		!errors.As(transport.Err, &operation) || operation.Op != "dial" || operation.Net != "tcp" ||
		operation.Timeout() || !errors.Is(operation.Err, hookConnectionRefused) {
		return false
	}
	target, ok := operation.Addr.(*net.TCPAddr)
	return ok && target.IP.Equal(net.ParseIP("127.0.0.1")) && target.Port == port
}

// Test seam for launch admission; production always uses the existing launcher.
var hookLocalServerLaunch = launchServeBackgroundContext

func recoverHookLocalServer(ctx context.Context, client *http.Client, base string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := hostBindingDataDirFromEnvOrConfig()
	if dir == "" {
		return errHookSessionUnconfirmed
	}
	expected, err := store.EnsureInstanceIDContext(ctx, dir)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// SessionStart enables automatic sync for shared serve. Pass this intent to
	// the child only; never mutate the hook's environment or cloud configuration.
	env := os.Environ()
	env = append(env, "ENGRAM_DATA_DIR="+dir, "ENGRAM_CLOUD_AUTOSYNC=1")
	if err = hookLocalServerLaunch(ctx, executable, filepath.Join(dir, "serve.err.log"), env); err != nil {
		return err
	}
	return hookLocalServerReady(ctx, client, base, expected)
}

func hookLocalServerReady(ctx context.Context, client *http.Client, base, expected string) error {
	// Do not follow health redirects or accept another listener's identity.
	healthClient := *client
	healthClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		if err != nil {
			return err
		}
		response, err := healthClient.Do(request)
		if err == nil && response.StatusCode == http.StatusInternalServerError {
			// Startup stats can fail transiently; this is readiness polling,
			// never permission to launch again or accept an unknown identity.
			_ = response.Body.Close()
		} else if err == nil {
			var health struct {
				Status     string `json:"status"`
				Service    string `json:"service"`
				InstanceID string `json:"instance_id"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, hookDenialBodyLimit)).Decode(&health)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK || decodeErr != nil || health.Status != "ok" || health.Service != "engram" || health.InstanceID != expected {
				return errors.New("managed local server identity unconfirmed")
			}
			return ctx.Err()
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
