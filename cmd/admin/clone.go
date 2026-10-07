package main

import (
	"context"
	"flag"
	"fmt"
	"hoanxu/internal/localclone"
	"os"
	"path/filepath"
	"time"
)

func cloneLocal(args []string) error {
	fs := flag.NewFlagSet("clone-local", flag.ContinueOnError)
	bin := fs.String("pg-bin", "", "directory containing pg_dump/pg_restore")
	dir := fs.String("destination", "", "new private backup directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join("private-data", "backups", localclone.BackupName())
	}
	source := os.Getenv("MIGRATION_DATABASE_URL")
	if source == "" {
		source = os.Getenv("DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	m, err := localclone.Run(ctx, source, os.Getenv("LOCAL_DATABASE_URL"), *bin, *dir)
	if err != nil {
		return err
	}
	fmt.Printf("Local clone verified: version=%d dirty=%t tables=%d ledger_mismatches=%d backup=%s\n", m.Version, m.Dirty, len(m.Tables), m.LedgerMismatches, *dir)
	return nil
}

func verifyLocal(args []string) error {
	fs := flag.NewFlagSet("verify-local", flag.ContinueOnError)
	dir := fs.String("destination", "", "private snapshot directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m, err := localclone.VerifyExisting(ctx, os.Getenv("LOCAL_DATABASE_URL"), *dir)
	if err != nil {
		return err
	}
	fmt.Printf("Local clone verified: version=%d dirty=%t tables=%d ledger_mismatches=%d\n", m.Version, m.Dirty, len(m.Tables), m.LedgerMismatches)
	return nil
}
func checkLocalHistory(args []string) error {
	fs := flag.NewFlagSet("check-local-history", flag.ContinueOnError)
	dir := fs.String("baseline", "", "verified clone backup directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--baseline is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tables, missing, err := localclone.VerifyHistory(ctx, os.Getenv("DATABASE_URL"), *dir, os.Getenv("PRIVATE_DIR"))
	if err != nil {
		return err
	}
	fmt.Printf("Historical tables verified=%d ledger_mismatches=0 missing_private_files=%d\n", tables, missing)
	return nil
}
