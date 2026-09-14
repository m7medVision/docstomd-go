GO ?= go
BENCH_VENV ?= bench/.cache/venv/bin/python

.PHONY: build test lint fmt clean bench-test bench-test-integration bench-pdf-selftest bench-pdf bench-ocr bench-formats

build:
	$(GO) build ./...

test:
	$(GO) test ./...

lint:
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed; ran go vet only"; fi

fmt:
	$(GO) fmt ./...

clean:
	$(GO) clean -testcache

bench-test:
	@if [ -x "$(BENCH_VENV)" ] && $(BENCH_VENV) -c "import pytest" 2>/dev/null; then $(BENCH_VENV) -m pytest bench/tests -q -m "not integration"; \
	else uv run --quiet --with pytest python -m pytest bench/tests -q -m "not integration"; fi

bench-test-integration:
	@if [ -x "$(BENCH_VENV)" ] && $(BENCH_VENV) -c "import pytest" 2>/dev/null; then $(BENCH_VENV) -m pytest bench/tests -q -m integration; \
	else uv run --quiet --with pytest python -m pytest bench/tests -q -m integration; fi

bench-pdf-selftest: bench-test
	python3 bench/pdf/run.py --self-test

bench-pdf:
	python3 bench/pdf/run.py $(BENCH_ARGS)

bench-ocr:
	python3 bench/ocr/run.py $(BENCH_ARGS)

bench-formats:
	python3 bench/formats/run.py $(BENCH_ARGS)
