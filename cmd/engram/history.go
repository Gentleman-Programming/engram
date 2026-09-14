package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const (
	defaultHistoryLimit = 10
	maxHistoryLimit     = 20
)

type historyCursor struct {
	SyncID        string `json:"sync_id"`
	RevisionCount int    `json:"revision_count"`
	VersionID     string `json:"version_id"`
}

type historyOptions struct {
	limit  int
	cursor string
	json   bool
}

type historyPage struct {
	ObservationID int64            `json:"observation_id"`
	Versions      []historyVersion `json:"versions"`
	HasMore       bool             `json:"has_more"`
	NextCursor    string           `json:"next_cursor,omitempty"`
}

type historyVersion struct {
	SessionID       string  `json:"session_id"`
	Type            string  `json:"type"`
	Title           string  `json:"title"`
	Content         string  `json:"content"`
	ToolName        *string `json:"tool_name,omitempty"`
	Project         *string `json:"project,omitempty"`
	Scope           string  `json:"scope"`
	TopicKey        *string `json:"topic_key,omitempty"`
	RevisionCount   int     `json:"revision_count"`
	IsBaseline      bool    `json:"is_baseline"`
	HistoryComplete bool    `json:"history_complete"`
	CapturedAt      string  `json:"captured_at"`
}

func cmdHistory(cfg store.Config) {
	if len(os.Args) < 3 {
		failHistory("observation ID is required")
		return
	}
	observationID, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil || observationID < 1 {
		failHistory(fmt.Sprintf("invalid observation ID %q", os.Args[2]))
		return
	}
	opts, err := parseHistoryOptions(os.Args[3:])
	if err != nil {
		failHistory(err.Error())
		return
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer s.Close()
	observation, err := s.GetObservation(observationID)
	if err != nil {
		fatal(err)
		return
	}
	var cursor *historyCursor
	if opts.cursor != "" {
		decoded, err := decodeHistoryCursor(s.InstanceID(), opts.cursor)
		if err != nil {
			failHistory("invalid history cursor")
			return
		}
		cursor = &decoded
	}
	if cursor != nil && cursor.SyncID != observation.SyncID {
		failHistory("history cursor does not belong to this observation")
		return
	}

	beforeRevision, beforeVersionID := 0, ""
	if cursor != nil {
		beforeRevision, beforeVersionID = cursor.RevisionCount, cursor.VersionID
	}
	versions, err := s.ObservationVersionsPage(observation.SyncID, beforeRevision, beforeVersionID, opts.limit+1)
	if errors.Is(err, store.ErrObservationNotFound) && cursor == nil {
		writeHistoryPage(historyPage{ObservationID: observationID, Versions: []historyVersion{}}, opts.json)
		return
	}
	if err != nil {
		fatal(fmt.Errorf("read observation history: %w", err))
		return
	}
	page := historyPage{
		ObservationID: observationID,
		HasMore:       len(versions) > opts.limit,
	}
	if page.HasMore {
		versions = versions[:opts.limit]
		page.NextCursor, err = encodeHistoryCursor(s.InstanceID(), observation.SyncID, versions[len(versions)-1])
		if err != nil {
			fatal(fmt.Errorf("encode history cursor: %w", err))
			return
		}
	}
	page.Versions = makeHistoryVersions(versions)
	writeHistoryPage(page, opts.json)
}

func parseHistoryOptions(args []string) (historyOptions, error) {
	opts := historyOptions{limit: defaultHistoryLimit}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			opts.json = true
		case "--limit":
			if i+1 >= len(args) {
				return historyOptions{}, errors.New("--limit requires a value")
			}
			i++
			limit, err := strconv.Atoi(args[i])
			if err != nil || limit < 1 || limit > maxHistoryLimit {
				return historyOptions{}, fmt.Errorf("--limit must be between 1 and %d", maxHistoryLimit)
			}
			opts.limit = limit
		case "--cursor":
			if i+1 >= len(args) {
				return historyOptions{}, errors.New("--cursor requires a value")
			}
			i++
			opts.cursor = args[i]
		default:
			return historyOptions{}, fmt.Errorf("unknown history argument %q", args[i])
		}
	}
	return opts, nil
}

func makeHistoryVersions(versions []store.ObservationVersion) []historyVersion {
	page := make([]historyVersion, len(versions))
	for i, version := range versions {
		page[i] = historyVersion{
			SessionID:       version.SessionID,
			Type:            version.Type,
			Title:           version.Title,
			Content:         version.Content,
			ToolName:        version.ToolName,
			Project:         version.Project,
			Scope:           version.Scope,
			TopicKey:        version.TopicKey,
			RevisionCount:   version.RevisionCount,
			IsBaseline:      version.IsBaseline,
			HistoryComplete: version.HistoryComplete,
			CapturedAt:      version.CapturedAt,
		}
	}
	return page
}

func decodeHistoryCursor(instanceID, encoded string) (historyCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return historyCursor{}, err
	}
	var cursor historyCursor
	aead := historyCursorAEAD(instanceID)
	if len(decoded) <= aead.NonceSize() {
		return historyCursor{}, errors.New("invalid cursor")
	}
	decoded, err = aead.Open(nil, decoded[:aead.NonceSize()], decoded[aead.NonceSize():], nil)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.RevisionCount < 1 {
		return historyCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func encodeHistoryCursor(instanceID, syncID string, version store.ObservationVersion) (string, error) {
	aead := historyCursorAEAD(instanceID)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(historyCursor{SyncID: syncID, RevisionCount: version.RevisionCount, VersionID: version.VersionID})
	return base64.RawURLEncoding.EncodeToString(append(nonce, aead.Seal(nil, nonce, payload, nil)...)), nil
}

func historyCursorAEAD(instanceID string) cipher.AEAD {
	key := sha256.Sum256([]byte("engram/history-cursor/v1:" + instanceID))
	block, _ := aes.NewCipher(key[:])
	aead, _ := cipher.NewGCM(block)
	return aead
}

func writeHistoryPage(page historyPage, jsonOut bool) {
	if jsonOut {
		out, err := jsonMarshalIndent(page, "", "  ")
		if err != nil {
			fatal(err)
			return
		}
		fmt.Println(string(out))
		return
	}
	if len(page.Versions) == 0 {
		fmt.Printf("No history snapshots are available for observation #%d.\n", page.ObservationID)
		return
	}
	fmt.Printf("Observation #%d history\n\n", page.ObservationID)
	for _, version := range page.Versions {
		fmt.Printf("Revision %d — %s\n", version.RevisionCount, version.CapturedAt)
		fmt.Printf("  [%s] %s\n  %s\n", version.Type, version.Title, version.Content)
		if version.IsBaseline {
			fmt.Println("  Baseline snapshot")
		}
		if !version.HistoryComplete {
			fmt.Println("  History may be incomplete")
		}
		fmt.Println()
	}
	if page.HasMore {
		fmt.Printf("More history is available. Continue with: engram history %d --cursor %s\n", page.ObservationID, page.NextCursor)
	}
}

func failHistory(message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	printHistoryUsage()
	exitFunc(1)
}

func printHistoryUsage() {
	fmt.Fprintln(os.Stdout, "usage: engram history <observation_id> [--limit N] [--cursor CURSOR] [--json]")
}
