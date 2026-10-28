package main

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/Gentleman-Programming/engram/v3/internal/mcp"
)

// A failed read is unavailable evidence, not an observed metadata violation.
// Keep the legacy diagnostic while retaining the observation's type.
type metadataUnavailable struct{ message string }

func (e *metadataUnavailable) Error() string { return e.message }

// A successfully written assessment is still a nonzero CLI result, but must
// not pass through the legacy stderr diagnostic path.
type assessmentExit struct{ code int }

func (e *assessmentExit) Error() string { return "candidate assessment is non-approving" }

func assessCandidate(root, version, piVersion string, output io.Writer) error {
	return assessCandidateWithContract(root, version, piVersion, output, mcp.AgentContractJSON)
}

// The per-call seam keeps production bound to the live Go exporter.
func assessCandidateWithContract(root, version, piVersion string, output io.Writer, exportContract func() ([]byte, error)) error {
	report := struct {
		SchemaVersion int    `json:"schemaVersion"`
		Version       string `json:"version"`
		PiVersion     string `json:"piVersion"`
		Outcome       string `json:"outcome"`
		Reason        struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"reason"`
	}{SchemaVersion: 1, Version: version, PiVersion: piVersion, Outcome: "blocked"}
	code := 2
	report.Reason.Code = "positive-proof-unavailable"
	report.Reason.Message = "live Go contract does not establish Pi parity or release approval"
	if err := validateMetadata(root, piVersion); err != nil {
		var unavailable *metadataUnavailable
		if errors.As(err, &unavailable) {
			report.Reason.Code = "metadata-unavailable"
		} else {
			report.Outcome, code = "rejected", 1
			report.Reason.Code = "invalid-metadata"
		}
		// validateMetadata exposes only fixed, repository-owned diagnostics.
		report.Reason.Message = err.Error()
	} else if _, err := exportContract(); err != nil {
		report.Reason.Code = "contract-unavailable"
		report.Reason.Message = "cannot export live Go agent contract"
	}
	// Exportability is not positive evidence: no trusted producer/binding is
	// available. Even a future contract status alone cannot approve this pair.
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if _, err := output.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return &assessmentExit{code: code}
}
