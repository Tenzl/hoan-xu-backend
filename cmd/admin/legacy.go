package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"hoanxu/internal/envguard"
	"hoanxu/internal/legacyimport"
)

func importLegacy(args []string) error {
	fs := flag.NewFlagSet("import-legacy", flag.ContinueOnError)
	file := fs.String("file", "", "Markdown customer export")
	apply := fs.Bool("apply", false, "commit customers, approved orders and wallet credits")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: admin import-legacy --file <Markdown file> [--apply]")
	}
	input, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer input.Close()
	plan, err := legacyimport.Prepare(input)
	if err != nil {
		return err
	}
	summary := plan.Summary()
	if *apply {
		if err := envguard.SeedAllowed(os.Getenv("DATABASE_URL"), os.Getenv("APP_ENV")); err != nil {
			return err
		}
		if os.Getenv("DATABASE_URL") == "" {
			return fmt.Errorf("DATABASE_URL is required for --apply")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return err
		}
		defer pool.Close()
		summary, err = plan.Apply(ctx, pool)
		if err != nil {
			return err
		}
	}
	result := struct {
		legacyimport.Summary
		Applied bool `json:"applied"`
	}{summary, *apply}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
