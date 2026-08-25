package diagnostic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusBlocked = "blocked"
	StatusError   = "error"

	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityBlocking = "blocking"
)

type Scope struct {
	Store                  *store.Store
	Project                string
	Now                    time.Time
	ReadSQLiteLockSnapshot func(context.Context) (store.SQLiteLockSnapshot, error)
	DetectProject          func(string) (DetectedProject, bool)
}

type DetectedProject struct {
	Project string `json:"project"`
	Source  string `json:"source"`
	Path    string `json:"path,omitempty"`
}

type Finding struct {
	CheckID              string          `json:"check_id"`
	Severity             string          `json:"severity"`
	ReasonCode           string          `json:"reason_code"`
	Message              string          `json:"message"`
	Why                  string          `json:"why"`
	Evidence             json.RawMessage `json:"evidence"`
	SafeNextStep         string          `json:"safe_next_step"`
	RequiresConfirmation bool            `json:"requires_confirmation"`
}

type CheckResult struct {
	CheckID              string          `json:"check_id"`
	Result               string          `json:"result"`
	Severity             string          `json:"severity"`
	ReasonCode           string          `json:"reason_code"`
	Message              string          `json:"message"`
	Why                  string          `json:"why"`
	Evidence             json.RawMessage `json:"evidence"`
	SafeNextStep         string          `json:"safe_next_step"`
	RequiresConfirmation bool            `json:"requires_confirmation"`
	Findings             []Finding       `json:"findings,omitempty"`
}

type Summary struct {
	Total    int `json:"total"`
	OK       int `json:"ok"`
	Warnings int `json:"warnings"`
	Blocked  int `json:"blocked"`
	Errors   int `json:"errors"`
}

type Report struct {
	Status  string        `json:"status"`
	Project string        `json:"project,omitempty"`
	Summary Summary       `json:"summary"`
	Checks  []CheckResult `json:"checks"`
}

type DiagnosticCheck interface {
	Code() string
	Run(context.Context, Scope) (CheckResult, error)
}

type Runner struct {
	registry Registry
}

func NewRunner() Runner                       { return Runner{registry: DefaultRegistry()} }
func NewRunnerWithRegistry(r Registry) Runner { return Runner{registry: r} }

func (r Runner) RunAll(ctx context.Context, scope Scope) (Report, error) {
	checks := r.registry.Checks()
	results := make([]CheckResult, 0, len(checks))
	for _, check := range checks {
		result, err := runCheck(ctx, scope, check)
		if err != nil {
			return Report{}, err
		}
		results = append(results, result)
	}
	return buildReport(scope.Project, results), nil
}

func (r Runner) RunOne(ctx context.Context, scope Scope, code string) (Report, error) {
	check, err := r.registry.Lookup(code)
	if err != nil {
		return Report{}, err
	}
	result, err := runCheck(ctx, scope, check)
	if err != nil {
		return Report{}, err
	}
	return buildReport(scope.Project, []CheckResult{result}), nil
}

func runCheck(ctx context.Context, scope Scope, check DiagnosticCheck) (CheckResult, error) {
	result, err := check.Run(ctx, scope)
	if err != nil {
		return CheckResult{}, err
	}
	result.CheckID = strings.TrimSpace(result.CheckID)
	if result.CheckID == "" {
		result.CheckID = check.Code()
	}
	if result.Result == "" {
		result.Result = StatusOK
	}
	if result.Severity == "" {
		result.Severity = SeverityInfo
	}
	if result.ReasonCode == "" {
		result.ReasonCode = result.CheckID + "_ok"
	}
	if result.Evidence == nil {
		result.Evidence = mustJSON(map[string]any{"evaluated": true})
	}
	return result, nil
}

func buildReport(project string, checks []CheckResult) Report {
	report := Report{Status: StatusOK, Project: project, Checks: checks}
	report.Summary.Total = len(checks)
	for _, check := range checks {
		switch check.Result {
		case StatusError:
			report.Summary.Errors++
		case StatusBlocked:
			report.Summary.Blocked++
		case StatusWarning:
			report.Summary.Warnings++
		default:
			report.Summary.OK++
		}
	}
	switch {
	case report.Summary.Errors > 0:
		report.Status = StatusError
	case report.Summary.Blocked > 0:
		report.Status = StatusBlocked
	case report.Summary.Warnings > 0:
		report.Status = StatusWarning
	default:
		report.Status = StatusOK
	}
	return report
}

func ErrorReport(project string, err error) Report {
	code := "diagnostic_error"
	if errors.Is(err, ErrInvalidCheck) {
		code = "invalid_check"
	}
	return Report{
		Status:  StatusError,
		Project: project,
		Summary: Summary{Total: 1, Errors: 1},
		Checks: []CheckResult{{
			CheckID:              code,
			Result:               StatusError,
			Severity:             SeverityError,
			ReasonCode:           code,
			Message:              err.Error(),
			Why:                  "Doctor could not run the requested diagnostic safely.",
			Evidence:             mustJSON(map[string]any{"error": err.Error()}),
			SafeNextStep:         "Run `engram doctor` without --check to list registered diagnostics.",
			RequiresConfirmation: false,
		}},
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func okResult(checkID string, evidence any) CheckResult {
	return CheckResult{
		CheckID:              checkID,
		Result:               StatusOK,
		Severity:             SeverityInfo,
		ReasonCode:           checkID + "_ok",
		Message:              "No issues detected.",
		Why:                  "The evaluated store evidence matches expected operational invariants.",
		Evidence:             mustJSON(evidence),
		SafeNextStep:         "No action required.",
		RequiresConfirmation: false,
	}
}

func resultFromFindings(checkID string, okEvidence any, findings []Finding) CheckResult {
	if len(findings) == 0 {
		return okResult(checkID, okEvidence)
	}
	result := CheckResult{
		CheckID:              checkID,
		Result:               StatusWarning,
		Severity:             SeverityWarning,
		ReasonCode:           findings[0].ReasonCode,
		Message:              fmt.Sprintf("%d finding(s) detected.", len(findings)),
		Why:                  findings[0].Why,
		Evidence:             mustJSON(map[string]any{"finding_count": len(findings)}),
		SafeNextStep:         findings[0].SafeNextStep,
		RequiresConfirmation: false,
		Findings:             findings,
	}
	for _, f := range findings {
		switch f.Severity {
		case SeverityBlocking:
			result.Result = StatusBlocked
			result.Severity = SeverityBlocking
			return result
		case SeverityError:
			if result.Result != StatusBlocked {
				result.Result = StatusError
				result.Severity = SeverityError
			}
		}
	}
	return result
}
