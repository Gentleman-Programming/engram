package store

import "strings"

// CapturePromptForSave applies the shared per-save prompt policy. An omitted
// option enables capture; false or missing prompt content is a no-op. The
// caller supplies the current prompt for the observation's session/project;
// persisted history is not a substitute for current runtime context.
// Call only after a successful observation save, and treat capture errors as
// nonfatal. This stores an independent prompt row, not an observation-ID link.
func (s *Store) CapturePromptForSave(enabled *bool, prompt AddPromptParams) error {
	if (enabled != nil && !*enabled) || strings.TrimSpace(prompt.Content) == "" {
		return nil
	}
	_, _, err := s.AddPromptIfMissing(prompt)
	return err
}
