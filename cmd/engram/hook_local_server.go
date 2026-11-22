package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func hookLocalWindowsPort(platform, base string) int {
	if platform != "windows" || strings.TrimSpace(os.Getenv("ENGRAM_URL")) != "" || strings.TrimSpace(os.Getenv("ENGRAM_SOCKET")) != "" || base != codexHookURL() {
		return 0
	}
	u, parseErr := url.Parse(base)
	if parseErr != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return 0
	}
	port, parseErr := strconv.Atoi(u.Port())
	if parseErr != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

// Eligibility describes the original lookup only; readiness is never an excuse
// to recover from a received HTTP response or a redirected request.
func hookLocalRecoveryEligible(platform, base, endpoint string, err error) bool {
	port := hookLocalWindowsPort(platform, base)
	if port == 0 {
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

// Match serve's home fallback without changing best-effort host binding policy.
func hookLocalDataDir() string {
	if dir := hostBindingDataDirFromEnvOrConfig(); dir != "" {
		return dir
	}
	if home := resolveHomeFallback(); home != "" {
		return filepath.Join(home, ".engram")
	}
	return ""
}

// A reachable loopback listener may belong to another data directory or OS
// account. Confirm identity before disclosing any session or cwd to it.
func confirmHookLocalIdentity(ctx context.Context, client *http.Client, base, platform string) error {
	dir := hookLocalDataDir()
	if dir == "" {
		return &hookDenialError{message: "managed local server identity unconfirmed"}
	}
	if err := ctx.Err(); err != nil {
		return &hookDenialError{message: "managed local server identity unconfirmed", cause: err}
	}
	managedWindows := hookLocalWindowsPort(platform, base) != 0
	var expected string
	if managedWindows {
		// A ready listener may share its store with a process holding the
		// initialization lock. Read the published marker before waiting.
		expected, _ = store.ReadPublishedInstanceID(dir)
	}
	if expected == "" {
		var err error
		expected, err = store.EnsureInstanceIDContext(ctx, dir)
		if err != nil {
			return &hookDenialError{message: "managed local server identity unconfirmed", cause: err}
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return &hookDenialError{message: "managed local server identity unconfirmed", cause: err}
	}
	healthClient := *client
	healthClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := healthClient.Do(request)
	if err != nil {
		return &hookDenialError{message: "managed local server identity unconfirmed", cause: err}
	}
	health := readHookLocalHealth(response)
	_ = response.Body.Close()
	if health.Status == "starting" && managedWindows {
		// Another hook can reach the early listener before the store is ready.
		// Wait for its identity without launching or sending session traffic.
		return hookLocalServerReady(ctx, client, base, expected)
	}
	if !health.matches(expected) {
		return hookLocalIdentityMismatch(health)
	}
	return nil
}

func recoverHookLocalServer(ctx context.Context, client *http.Client, base string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := hookLocalDataDir()
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
		if err == nil {
			health := readHookLocalHealth(response)
			_ = response.Body.Close()
			switch {
			case health.Status == "starting", response.StatusCode == http.StatusInternalServerError:
				// The early listener and transient startup stats are readiness
				// signals only. Neither establishes the instance's identity.
			case health.matches(expected):
				return ctx.Err()
			default:
				return hookLocalIdentityMismatch(health)
			}
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

type hookLocalHealth struct {
	Status     string          `json:"status"`
	Service    string          `json:"service"`
	InstanceID string          `json:"instance_id"`
	Version    json.RawMessage `json:"version"`
}

func hookLocalIdentityMismatch(health hookLocalHealth) error {
	const denial = "managed local server identity unconfirmed: expected current data directory instance"
	return &hookDenialError{message: denial + health.releaseSkewGuidance(version)}
}

// Initial admission and managed recovery share the same bounded health reader.
func readHookLocalHealth(response *http.Response) hookLocalHealth {
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return hookLocalHealth{}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, hookDenialBodyLimit+1))
	if err != nil || len(body) > hookDenialBodyLimit {
		return hookLocalHealth{}
	}
	var health hookLocalHealth
	if json.Unmarshal(body, &health) != nil || health.Service != "engram" {
		return hookLocalHealth{}
	}
	if response.StatusCode == http.StatusOK && health.Status == "ok" {
		return health
	}
	if response.StatusCode == http.StatusServiceUnavailable && health.Status == "starting" && health.InstanceID == "" {
		return hookLocalHealth{Status: "starting"}
	}
	return hookLocalHealth{}
}

func (health hookLocalHealth) matches(expected string) bool {
	return expected != "" && health.InstanceID == expected
}

func (health hookLocalHealth) releaseSkewGuidance(installed string) string {
	var reported string
	// Keep untrusted version metadata short before including it in the denial.
	if json.Unmarshal(health.Version, &reported) != nil || len(reported) > 64 {
		return ""
	}
	reported, installed = normalizeDaemonRelease(reported), normalizeDaemonRelease(installed)
	if reported == "" || installed == "" || reported == installed {
		return ""
	}
	return fmt.Sprintf(". Listener reports server %s (installed CLI %s). Verify the listener and its data directory, then restart the intended server through its existing supervisor or launch method.", reported, installed)
}
