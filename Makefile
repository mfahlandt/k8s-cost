SHELL := /bin/bash
DATA  ?= ./data
ASOF  ?=

.PHONY: build test report web dev clean tidy

## build the costctl CLI into ./bin
build:
	go build -o bin/costctl ./cmd/costctl

## run Go tests
test:
	go test ./...

## regenerate dashboard.json + XLSX from the committed data store
report:
	go run ./cmd/costctl report $(if $(ASOF),--asof $(ASOF),) --data $(DATA) \
		--json web/public/dashboard.json --xlsx reports/report.xlsx

## build the static frontend (outputs web/dist)
web:
	cd web && npm ci && npm run build

## run the frontend dev server
dev:
	cd web && npm install && npm run dev

tidy:
	go mod tidy

clean:
	rm -rf bin web/dist web/node_modules

