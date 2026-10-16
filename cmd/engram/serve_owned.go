package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
)

const sessionOwnedProtocol = "engram-session-owned-v1"

// Presence is intentional: even a blank explicit transport is a conflict.
func validateSessionOwned(args []string) (bool, error) {
	owned := false
	for _, arg := range args {
		if arg == "--session-owned" {
			owned = true
		}
	}
	if !owned {
		return false, nil
	}
	for _, key := range []string{"ENGRAM_URL", "ENGRAM_PORT", "ENGRAM_SOCKET"} {
		if _, ok := os.LookupEnv(key); ok {
			return true, fmt.Errorf("session-owned serve conflicts with %s; unset it", key)
		}
	}
	for _, arg := range args {
		if arg != "--session-owned" {
			return true, fmt.Errorf("session-owned serve accepts no port or socket arguments")
		}
	}
	token := os.Getenv("ENGRAM_TOKEN")
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return true, fmt.Errorf("session-owned serve requires a private 256-bit hex ENGRAM_TOKEN")
	}
	return true, nil
}

func watchOwnedParent(input io.Reader, cancel func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, input)
		cancel()
		close(done)
	}()
	return done
}

func emitOwnedReadiness(addr net.Addr, instanceID string) error {
	record := struct {
		Protocol   string `json:"protocol"`
		Endpoint   string `json:"endpoint"`
		PID        int    `json:"pid"`
		InstanceID string `json:"instance_id"`
	}{sessionOwnedProtocol, "http://" + addr.String(), os.Getpid(), instanceID}
	return json.NewEncoder(os.Stdout).Encode(record)
}
