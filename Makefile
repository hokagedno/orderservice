.PHONY: help proto build run-api run-inventory test test-race fmt up down logs psql grpcurl

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

proto: ## Перегенерировать код из .proto
	protoc --proto_path=api/proto \
		--go_out=. --go_opt=module=github.com/hokagedno/orderservice \
		--go-grpc_out=. --go-grpc_opt=module=github.com/hokagedno/orderservice \
		api/proto/inventory/v1/inventory.proto

build: ## Собрать оба бинарника
	go build -o bin/orderapi ./cmd/orderapi
	go build -o bin/inventory ./cmd/inventory

run-inventory: ## Запустить gRPC-сервис склада
	go run ./cmd/inventory

run-api: ## Запустить REST-сервис заказов
	go run ./cmd/orderapi

test: ## Тесты
	go test ./... -count=1

test-race: ## Тесты с детектором гонок
	go test -race ./... -count=1

fmt: ## Форматирование и go vet
	gofmt -w .
	go vet ./...

up: ## Поднять окружение в Docker
	docker compose up --build -d

down: ## Остановить окружение
	docker compose down -v

logs: ## Логи сервиса заказов
	docker compose logs -f orderapi

psql: ## Консоль PostgreSQL
	docker compose exec postgres psql -U orders -d orders

grpcurl: ## Пример вызова gRPC через grpcurl (нужен установленный grpcurl)
	grpcurl -plaintext localhost:9090 list
	grpcurl -plaintext -d '{"sku":"MOUSE-01"}' localhost:9090 inventory.v1.InventoryService/GetStock
