.PHONY: help build test lint lint-fix format run dev clean install-tools digest-now check-deps

APP      := bot-news
BUILD_DIR := build
MAIN_PATH := ./cmd/$(APP)

GREEN  := \033[0;32m
YELLOW := \033[1;33m
NC     := \033[0m

help: ## Показать справку
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  $(YELLOW)%-15s$(NC) %s\n", $$1, $$2}'

install-tools: ## Установить инструменты разработки
	@echo "$(GREEN)Установка golangci-lint...$(NC)"
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
	@echo "$(GREEN)Установка goimports...$(NC)"
	@go install golang.org/x/tools/cmd/goimports@latest
	@echo "$(GREEN)Установка air...$(NC)"
	@go install github.com/air-verse/air@latest

build: ## Собрать бинарник
	@echo "$(GREEN)Сборка...$(NC)"
	@mkdir -p $(BUILD_DIR)
	@go build -ldflags="-s -w" -o $(BUILD_DIR)/$(APP) $(MAIN_PATH)
	@echo "$(GREEN)Готово: $(BUILD_DIR)/$(APP)$(NC)"

test: ## Запустить тесты с покрытием
	@echo "$(GREEN)Тесты...$(NC)"
	@go test -v -race -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)Покрытие: coverage.html$(NC)"

lint: ## Запустить golangci-lint
	@echo "$(GREEN)Линтер...$(NC)"
	@golangci-lint run --timeout=5m

lint-fix: ## Автоисправление проблем кода
	@gofmt -w .
	@goimports -w .

format: lint-fix ## Форматировать код

check: lint test ## Линтер + тесты

run: build ## Собрать и запустить
	@echo "$(GREEN)Запуск...$(NC)"
	@./$(BUILD_DIR)/$(APP)

dev: ## Запуск с автоперезагрузкой (air)
	@echo "$(GREEN)Dev-режим...$(NC)"
	@air

digest-now: ## Немедленно сформировать и отправить дайджест
	@go run $(MAIN_PATH) --run-digest-now

check-deps: ## Проверить зависимости
	@go mod verify
	@go mod tidy

clean: ## Удалить артефакты сборки
	@rm -rf $(BUILD_DIR) tmp coverage.out coverage.html
