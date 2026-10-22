SHELL := /bin/bash
.SHELLFLAGS := -o pipefail -c

ROOT_DIR   := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))
SRC_DIR    := $(ROOT_DIR)/src
MIGRATIONS_DIR := $(ROOT_DIR)/src/migrations
CONFIG_FILE := $(ROOT_DIR)/config.yml
CONFIG_TEST_FILE := $(ROOT_DIR)/config_test.yml
BIN        := $(ROOT_DIR)/bin/transport
REVISION   ?= $(shell git describe --tags 2>/dev/null || git log --format="v0.0-%h" -n 1 || echo "v0.0-unknown")

# Куда go install кладёт бинарники (учитываем GOBIN, если выставлен)
GOBIN_DIR := $(shell go env GOBIN)
ifeq ($(GOBIN_DIR),)
GOBIN_DIR := $(shell go env GOPATH)/bin
endif
export PATH  := $(GOBIN_DIR):$(PATH)
export GOBIN := $(GOBIN_DIR)

GOCOV_VERSION := latest
JUNIT_VERSION := latest

.PHONY: build run run_docker build_no_fmt test jenkins_test test_reports \
        fmt deps migration migrate migrate_test migrate_down \
        clean transport_tool_install

build: deps fmt
	@echo "==> Building"
	@cd $(SRC_DIR) && CGO_ENABLED=0 go build -o $(BIN) -ldflags "-X common.build=$(REVISION)" .
	@echo $(BIN)

build_no_fmt:
	@echo "==> Building (no fmt)"
	@cd $(SRC_DIR) && CGO_ENABLED=0 go build -o $(BIN) -ldflags "-X common.build=$(REVISION)" .
	@echo $(BIN)

run: migrate
	@echo "==> Running"
	@$(BIN) --config $(CONFIG_FILE) run

# Цель для запуска в контейнере: без git-зависимости и без gofmt -w
run_docker: deps build_no_fmt
	@echo "==> Migrating"
	@$(BIN) --config $(CONFIG_FILE) migrate
	@echo "==> Running"
	@$(BIN) --config $(CONFIG_FILE) run

test: deps fmt
	@echo "==> Running tests"
	@cd $(ROOT_DIR) && go test ./... -v -cpu 2

jenkins_test: clean migrate_test
	@echo "==> Running tests"
	@CGO_ENABLED=1 go test ./... -v -cpu 2 -race -coverpkg=./... -coverprofile=$(ROOT_DIR)/coverage.out | tee $(ROOT_DIR)/test-report.txt
	@go install github.com/axw/gocov/gocov@$(GOCOV_VERSION)
	@gocov convert $(ROOT_DIR)/coverage.out | gocov report
	@go mod tidy

test_reports:
	@go install github.com/jstemmer/go-junit-report/v2@$(JUNIT_VERSION)
	@go-junit-report < $(ROOT_DIR)/test-report.txt > $(ROOT_DIR)/test-report.xml || true

fmt:
	@echo "==> Running gofmt"
	@gofmt -l -s -w $(ROOT_DIR)

deps:
	@echo "==> Installing dependencies"
	@go mod tidy

migration: transport_tool_install
	@transport-core-tool migration -d $(MIGRATIONS_DIR)

migrate: build
	@$(BIN) --config $(CONFIG_FILE) migrate

migrate_test: build
	@$(BIN) --config $(CONFIG_TEST_FILE) migrate

migrate_down: build
	@$(BIN) --config $(CONFIG_FILE) migrate -v down

clean:
	@rm -rf coverage.out test-report.txt test-report.xml
	@cp -fr config_test.yml.dist config_test.yml
