package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
)

// handlePromptCaptureDecision classifies Claude input without persisting or notifying writes.
func (s *Server) handlePromptCaptureDecision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source  *string `json:"source"`
		Cwd     *string `json:"cwd"`
		Content *string `json:"content"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		jsonError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Source == nil || *body.Source != "claude-code" || body.Cwd == nil || strings.TrimSpace(*body.Cwd) == "" || body.Content == nil {
		jsonError(w, http.StatusBadRequest, "source must be claude-code, cwd must be nonblank, and content must be a string")
		return
	}
	content := strings.TrimSpace(*body.Content)
	if content == "" || strings.HasPrefix(content, "<task-notification>") || strings.HasPrefix(content, "<agent-message") {
		jsonResponse(w, http.StatusOK, map[string]any{"decision": "skip"})
		return
	}
	cwd := strings.TrimSpace(*body.Cwd)
	res, err := projectpkg.Resolve(projectpkg.ResolutionOptions{Mode: projectpkg.ResolutionCurrent, Directory: cwd, Detect: s.store.InspectProject})
	if err != nil {
		s.writeProjectResolutionError(w, res, err)
		return
	}
	payload := currentProjectPayload(cwd, res)
	payload["decision"] = "capture"
	jsonResponse(w, http.StatusOK, payload)
}
