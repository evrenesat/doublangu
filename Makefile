.PHONY: dev fmt test verify smoke-health build

GO_SRC = ./cmd/doublangu-server ./internal/config ./internal/httpapi
GO_BIN = doublangu-server
SMOKE_PORT = 3099

dev:
	@echo "Starting Go server on port 8080…"
	@DOUBLANGU_PORT=8080 go run $(GO_SRC) </dev/null &>/tmp/doublangu-dev.log & SERVER_PID=$$!; \
	trap 'kill $$SERVER_PID 2>/dev/null; wait $$SERVER_PID 2>/dev/null; rm -f /tmp/doublangu-dev.log' INT TERM; \
	echo "Go server PID=$$SERVER_PID"; \
	cd web && npm run dev; \
	EXIT_CODE=$$?; \
	kill $$SERVER_PID 2>/dev/null; \
	wait $$SERVER_PID 2>/dev/null; \
	exit $$EXIT_CODE

fmt:
	go fmt ./...
	cd web && npm run format

test:
	go test ./... -count=1
	cd web && npm run test:unit

verify:
	go vet ./...
	go test ./... -count=1
	cd web && npm run check
	cd web && npm run test:unit
	cd web && npm run build

smoke-health:
	@echo "Starting server on port $(SMOKE_PORT)…"
	@DOUBLANGU_HOST=127.0.0.1 DOUBLANGU_PORT=$(SMOKE_PORT) go run $(GO_SRC) </dev/null &>/tmp/doublangu-smoke.log & SERVER_PID=$$!; \
	trap 'kill $$SERVER_PID 2>/dev/null; wait $$SERVER_PID 2>/dev/null; rm -f /tmp/doublangu-smoke.log' INT TERM; \
	echo "Server PID=$$SERVER_PID"; \
	ELAPSED=0; \
	while [ $$ELAPSED -lt 15 ]; do \
		if curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:$(SMOKE_PORT)/health/live 2>/dev/null | grep -q 200; then \
			break; \
		fi; \
		sleep 0.5; \
		ELAPSED=$$((ELAPSED + 1)); \
	done; \
	echo "Checking /health/live…"; \
	HTTP_CODE=$$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:$(SMOKE_PORT)/health/live); \
	if [ "$$HTTP_CODE" != "200" ]; then \
		echo "FAIL: expected 200, got $$HTTP_CODE"; \
		kill $$SERVER_PID 2>/dev/null; \
		wait $$SERVER_PID 2>/dev/null; \
		exit 1; \
	fi; \
	BODY=$$(curl -s http://127.0.0.1:$(SMOKE_PORT)/health/live); \
	if ! echo "$$BODY" | grep -q '"ok"'; then \
		echo "FAIL: expected status ok"; \
		kill $$SERVER_PID 2>/dev/null; \
		wait $$SERVER_PID 2>/dev/null; \
		exit 1; \
	fi; \
	echo "PASS"; \
	EXIT_CODE=0; \
	kill $$SERVER_PID 2>/dev/null; \
	wait $$SERVER_PID 2>/dev/null; \
	exit $$EXIT_CODE

build:
	go build -o $(GO_BIN) ./cmd/doublangu-server
