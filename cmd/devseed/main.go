// Command devseed creates deterministic demo data for local UI/API work.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/devseed"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to the YAML config file (optional)")
	dbPath := flag.String("db", "", "SQLite database path (overrides server.db_path)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	path := *dbPath
	if path == "" {
		if cfg.StorageMode != config.StorageSQLite {
			log.Fatalf("seed-demo only supports sqlite local mode; set -db PATH or server.storage_mode: sqlite")
		}
		path = cfg.DBPath
	}

	summary, err := devseed.SeedSQLite(context.Background(), path)
	if err != nil {
		log.Fatalf("seed demo: %v", err)
	}
	fmt.Printf("Seeded demo data in %s\n", summary.DBPath)
	fmt.Printf("  session: %s\n", summary.SessionID)
	fmt.Printf("  story:   %s\n", summary.StoryID)
	fmt.Printf("  tasks:   %v\n", summary.TaskIDs)
}
