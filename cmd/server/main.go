package main

import (
	"context"
	"log"
	"os"

	"niche-service/internal/assets"
	"niche-service/internal/config"
	"niche-service/internal/db"
	"niche-service/internal/httpapi"
	"niche-service/internal/service"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	if cfg.RunMigrations {
		if err := db.ApplySQLDir(ctx, pool, assets.Migrations, "migrations"); err != nil {
			log.Fatalf("migrations: %v", err)
		}
	}
	if cfg.Seed {
		if err := db.ApplySQLDir(ctx, pool, assets.Seed, "seed"); err != nil {
			log.Fatalf("seed: %v", err)
		}
	}

	router := httpapi.NewRouter(service.New(pool), cfg.Tokens, os.Stdout)
	log.Printf("niche-service listening on %s", cfg.HTTPAddr)
	if err := router.Run(cfg.HTTPAddr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
