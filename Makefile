.PHONY: templ perf-check perf-baseline

templ:
	go tool templ generate ./internal/cloud/dashboard/...

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
