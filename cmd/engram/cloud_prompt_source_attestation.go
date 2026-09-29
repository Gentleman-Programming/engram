package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/v2/internal/cloudconfig"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const promptSourceAttestationUsage = "usage: engram cloud attest-prompt-source --sync-id <exact-sync-id> --owner-project <asserted-owner>"

type promptSourceAttester interface {
	AttestPromptSource(sessionID, sourceInboxID, syncID, ownerProject, promptProject string) (int64, error)
}

// The preview is observed data; the owner is a separate human assertion.
func attestPromptSource(s *store.Store, syncID, owner, endpoint string, remote promptSourceAttester, input io.Reader, output io.Writer) error {
	preview, found, err := s.PreviewPromptSource(syncID)
	if err != nil {
		return fmt.Errorf("prompt source preview failed: %w", err)
	}
	if !found {
		return fmt.Errorf("no unambiguous complete prompt source preview for exact sync ID")
	}
	if _, err := fmt.Fprintf(output, "Observed source: session=%q source_inbox=%q sync_id=%q prompt_project=%q kind=%q\nHuman-asserted owner: %q\n", preview.SessionID, preview.SourceInboxID, preview.SyncID, preview.Project, preview.Kind, owner); err != nil {
		return fmt.Errorf("attestation preview output failed: %w", err)
	}
	if _, err := fmt.Fprint(output, "Send this exact tuple for remote attestation? Type yes or no: "); err != nil {
		return fmt.Errorf("attestation confirmation question output failed: %w", err)
	}
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("confirmation input failed: %w", err)
	}
	if strings.TrimSpace(line) != "yes" {
		if strings.TrimSpace(line) == "no" {
			return fmt.Errorf("attestation declined; no remote call made")
		}
		return fmt.Errorf("invalid confirmation; type yes or no; no remote call made")
	}
	id, err := remote.AttestPromptSource(preview.SessionID, preview.SourceInboxID, preview.SyncID, owner, preview.Project)
	if err != nil {
		return fmt.Errorf("remote attestation failed: %w", err)
	}
	if id <= 0 {
		return fmt.Errorf("remote attestation returned no positive ID; local confirmation not saved")
	}
	if err := s.ConfirmPromptSourceAttestation(endpoint, preview, owner, id); err != nil {
		return fmt.Errorf("remote attestation succeeded (ID %d), but local confirmation failed: %w; do not assume local authorization", id, err)
	}
	if _, err := fmt.Fprintf(output, "Remote attestation ID %d confirmed locally for sync ID %q\n", id, syncID); err != nil {
		return fmt.Errorf("remote attestation confirmed locally, but success output failed: %w", err)
	}
	return nil
}

// Validate the endpoint before creating transport or persisting it as remote_target.
func promptSourceAttestationEndpoint(raw string) (string, error) {
	endpoint, err := cloudconfig.ValidateServerURL(raw)
	if err != nil {
		return "", fmt.Errorf("invalid cloud server URL")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil {
		return "", fmt.Errorf("cloud server URL credentials are not allowed")
	}
	return endpoint, nil
}

func cmdCloudAttestPromptSource(cfg store.Config) {
	args := os.Args[3:]
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Println(promptSourceAttestationUsage)
		return
	}
	if len(args) != 4 || args[0] != "--sync-id" || args[2] != "--owner-project" || strings.TrimSpace(args[1]) == "" || strings.TrimSpace(args[3]) == "" {
		fmt.Fprintln(os.Stderr, promptSourceAttestationUsage)
		fatal(fmt.Errorf("one exact sync ID and an asserted owner project are required"))
		return
	}
	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		fatal(fmt.Errorf("cloud runtime configuration unavailable"))
		return
	}
	if cc == nil || cc.ServerURL == "" {
		fatal(fmt.Errorf("cloud server URL is required"))
		return
	}
	endpoint, err := promptSourceAttestationEndpoint(cc.ServerURL)
	if err != nil {
		fatal(err)
		return
	}
	if strings.TrimSpace(cc.Token) == "" {
		fatal(fmt.Errorf("human bearer token required for remote dual-grant enforcement"))
		return
	}
	transport, err := remote.NewMutationTransport(endpoint, cc.Token)
	if err != nil {
		fatal(fmt.Errorf("cloud transport configuration rejected"))
		return
	}
	s, err := storeNew(cfg)
	if err != nil {
		fatal(fmt.Errorf("local store unavailable: %w", err))
		return
	}
	defer func() { _ = s.Close() }()
	if err := attestPromptSource(s, args[1], args[3], endpoint, transport, os.Stdin, os.Stdout); err != nil {
		fatal(err)
	}
}
