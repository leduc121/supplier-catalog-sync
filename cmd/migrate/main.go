package main

import (
	"context"
	"log"
	"os"
	"supplier-catalog-sync/internal/postgres"
	"time"
)

func main() {
	databaseURL := os.Getenv("DATABASE_DIRECT_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := postgres.Migrate(ctx, databaseURL); err != nil {
		log.Fatal(err)
	}
	log.Print("database migrations are up to date")
}
