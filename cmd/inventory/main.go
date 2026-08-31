// Команда inventory — gRPC-сервис склада.
package main

import (
	"log"

	"github.com/hokagedno/orderservice/internal/app"
)

func main() {
	if err := app.RunInventory(); err != nil {
		log.Fatalf("фатальная ошибка: %v", err)
	}
}
