// Command kaikki-import streams a Wiktextract/kaikki.org JSONL dictionary dump
// into tifl's shared reader definition cache.
package main

import (
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/dleiferives/tifl/internal/config"
	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/kaikki"
	"github.com/dleiferives/tifl/internal/lang"
	greekplugin "github.com/dleiferives/tifl/internal/lang/el"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "path to the YAML config file (optional)")
	inputPath := flag.String("input", "", "path to a Wiktextract JSONL file (.jsonl or .jsonl.gz)")
	languageCode := flag.String("language", "el", "target language code to import")
	datasetVersion := flag.String("dataset-version", "", "dataset version or dump date for the import audit row")
	dbPath := flag.String("db", "", "SQLite database path (overrides server.db_path and storage mode)")
	flag.Parse()

	if *inputPath == "" {
		log.Fatal("-input is required")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *dbPath != "" {
		cfg.StorageMode = config.StorageSQLite
		cfg.DBPath = *dbPath
	}

	ctx := context.Background()
	repo, err := openRepo(ctx, cfg)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	language, err := languageByCode(*languageCode)
	if err != nil {
		log.Fatal(err)
	}
	if err := repo.UpsertLanguage(ctx, domain.Language{
		Code:        language.Code(),
		Name:        language.Name(),
		KeyStrategy: string(language.KeyStrategy()),
		Enabled:     true,
	}); err != nil {
		log.Fatalf("seed language: %v", err)
	}

	in, closeInput, err := openInput(*inputPath)
	if err != nil {
		log.Fatalf("input: %v", err)
	}
	defer closeInput()

	importer, err := kaikki.NewImporter(repo, kaikki.Options{
		Language:       language,
		SourcePath:     *inputPath,
		DatasetVersion: *datasetVersion,
	})
	if err != nil {
		log.Fatal(err)
	}
	stats, err := importer.Import(ctx, in)
	if err != nil {
		log.Fatalf("import %s: %v", stats.ImportID, err)
	}

	fmt.Printf("Imported Wiktextract definitions into %s\n", cfg.StorageMode)
	fmt.Printf("  import_id:           %s\n", stats.ImportID)
	fmt.Printf("  language:            %s\n", language.Code())
	fmt.Printf("  entries_read:        %d\n", stats.EntriesRead)
	fmt.Printf("  entries_matched:     %d\n", stats.EntriesMatched)
	fmt.Printf("  definitions_written: %d\n", stats.DefinitionsWritten)
	fmt.Printf("  form_definitions:    %d\n", stats.FormDefinitions)
}

func openRepo(ctx context.Context, cfg config.Config) (db.Repository, error) {
	switch cfg.StorageMode {
	case config.StorageSQLite:
		return db.OpenSQLite(cfg.DBPath)
	case config.StoragePostgres:
		if cfg.DatabaseURL == "" {
			return nil, errors.New("postgres mode requires DATABASE_URL")
		}
		return db.OpenPostgres(ctx, cfg.DatabaseURL)
	default:
		return nil, fmt.Errorf("unknown storage mode %q", cfg.StorageMode)
	}
}

func languageByCode(code string) (lang.Language, error) {
	switch code {
	case "el":
		return greekplugin.New(), nil
	default:
		return nil, fmt.Errorf("unsupported language %q; registered import languages: el", code)
	}
}

func openInput(path string) (io.Reader, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = f.Close() }
	if !strings.HasSuffix(path, ".gz") {
		return f, cleanup, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return gz, func() {
		_ = gz.Close()
		cleanup()
	}, nil
}
