.PHONY: templ lint perf-check perf-baseline

GOLANGCI_LINT_VERSION := 2.13.2

templ:
	go tool templ generate ./internal/cloud/dashboard/...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint v$(GOLANGCI_LINT_VERSION) is required on PATH."; \
		exit 1; \
	}; \
	version="$$(golangci-lint version --short 2>/dev/null)" || { \
		echo "golangci-lint v$(GOLANGCI_LINT_VERSION) is required, but the executable on PATH does not support 'version --short'."; \
		exit 1; \
	}; \
	if [ "$$version" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "golangci-lint v$(GOLANGCI_LINT_VERSION) is required; found '$$version'."; \
		exit 1; \
	fi; \
	golangci-lint run --new

perf-check:
	@if [ -n "$(PERF_RATCHET_AGAINST)" ]; then \
		bash scripts/perf-ratchet.sh --against "$(PERF_RATCHET_AGAINST)"; \
	elif [ "$(PERF_RATCHET_BOOTSTRAP)" = "1" ]; then \
		bash scripts/perf-ratchet.sh --bootstrap; \
	else \
		bash scripts/perf-ratchet.sh; \
	fi

perf-baseline:
	bash scripts/perf-ratchet.sh --update
