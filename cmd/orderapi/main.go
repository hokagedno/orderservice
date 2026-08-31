// Команда orderapi — REST-сервис заказов.
package main

import (
	"log"

	"github.com/hokagedno/orderservice/internal/app"
)

func main() {
	if err := app.RunAPI(); err != nil {
		log.Fatalf("фатальная ошибка: %v", err)
	}
}
