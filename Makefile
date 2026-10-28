.PHONY: dev-setup dev-prepare dev-login deps-up deps-stop deps-status api worker migrate test test-branding test-integration build

dev-setup deps-up:
	./scripts/dev-setup.sh

dev-prepare:
	./scripts/dev-setup.sh prepare

dev-login:
	./scripts/dev-setup.sh login

# Stops only Compose-managed services. External data volume is retained.
deps-stop:
	docker compose --env-file .local/dev.env stop

deps-status:
	docker ps --filter name=unihub-dev --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'

api:
	@set -a; . ./.local/dev.env; set +a; go run ./cmd/unihub serve

worker:
	@set -a; . ./.local/dev.env; set +a; go run ./cmd/unihub worker

migrate:
	@set -a; . ./.local/dev.env; set +a; go run ./cmd/unihub migrate

test:
	go test ./...

test-branding:
	python3 scripts/test-branding.py

test-integration:
	@test -f .local/dev.env || (echo "missing .local/dev.env; run make dev-setup first" >&2; exit 1)
	@set -a; . ./.local/dev.env; set +a; UNIHUB_TEST_DATABASE_URL="$$DATABASE_URL" UNIHUB_TEST_LEDGER_URL="$$LEDGER_URL" go test -race -count=1 -timeout 90s ./...

build:
	go build -o bin/unihub ./cmd/unihub

.PHONY: web-build web-test
web-build:
	npm --prefix web ci
	npm --prefix web run build

web-test:
	npm --prefix web test
