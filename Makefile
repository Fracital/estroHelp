.PHONY: dev backend frontend build run test db-start db-stop

PG_DATA ?= .pgdata

build: frontend
	go build -o bin/elichat ./cmd/elichat

backend:
	go run ./cmd/elichat

frontend:
	cd frontend && npm install --no-audit --no-fund && npm run build

dev:
	@echo "Run 'make backend' and 'cd frontend && npm run dev' in two terminals; Astro proxies /api and /v1 to :3000"

run: build
	./bin/elichat

test:
	go vet ./... && go test ./...

db-start:
	@test -d $(PG_DATA) || initdb -D $(PG_DATA) -U postgres --auth=trust -E UTF8 >/dev/null
	pg_ctl -D $(PG_DATA) -l $(PG_DATA)/server.log -o "-k /tmp" start
	@psql -h /tmp -U postgres -tc "SELECT 1 FROM pg_database WHERE datname='elichat'" | grep -q 1 || psql -h /tmp -U postgres -c "CREATE DATABASE elichat"

db-stop:
	pg_ctl -D $(PG_DATA) stop
