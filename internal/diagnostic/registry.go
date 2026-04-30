package diagnostic

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrInvalidCheck = errors.New("invalid diagnostic check")

type Registry struct {
	checks map[string]DiagnosticCheck
	order  []string
}

func NewRegistry(checks ...DiagnosticCheck) Registry {
	r := Registry{checks: map[string]DiagnosticCheck{}}
	for _, check := range checks {
		if check == nil {
			continue
		}
		code := strings.TrimSpace(check.Code())
		if code == "" {
			continue
		}
		if _, exists := r.checks[code]; !exists {
			r.order = append(r.order, code)
		}
		r.checks[code] = check
	}
	sort.Strings(r.order)
	return r
}

func DefaultRegistry() Registry {
	return NewRegistry(
		SessionProjectDirectoryMismatchCheck{},
		ManualSessionNameProjectMismatchCheck{},
		SyncMutationRequiredFieldsCheck{},
		SQLiteLockContentionCheck{},
	)
}

func (r Registry) Checks() []DiagnosticCheck {
	out := make([]DiagnosticCheck, 0, len(r.order))
	for _, code := range r.order {
		out = append(out, r.checks[code])
	}
	return out
}

func (r Registry) Lookup(code string) (DiagnosticCheck, error) {
	code = strings.TrimSpace(code)
	if check, ok := r.checks[code]; ok {
		return check, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrInvalidCheck, code)
}

func RegisteredCodes() []string { return DefaultRegistry().order }
