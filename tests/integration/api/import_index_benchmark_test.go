package api

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Compare the worker's remaining-row lookup after 98% of a large batch is applied.
// The schema is disposable; this never changes an application database.
func TestImportValidIndexBenchmark(t *testing.T) {
	if os.Getenv("RUN_IMPORT_INDEX_BENCHMARK") != "1" {
		t.Skip("explicit index benchmark run")
	}
	s, _, admin := testStore(t)
	ctx := context.Background()
	var batch string
	if err := s.Pool.QueryRow(ctx, `INSERT INTO import_batches(actor_id,filename,file_hash,mapping) VALUES($1,'index-fixture.csv','fixture','{}') RETURNING id::text`, admin).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO import_rows(batch_id,row_number,payload,status) SELECT $1,i,'{}'::jsonb,CASE WHEN i>49000 THEN 'valid' ELSE 'applied' END FROM generate_series(1,50000) i`, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `ANALYZE import_rows`); err != nil {
		t.Fatal(err)
	}
	query := `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT row_number,payload FROM import_rows WHERE batch_id=$1 AND status='valid' ORDER BY row_number LIMIT 1`
	results := map[string]json.RawMessage{}
	var indexed []byte
	if err := s.Pool.QueryRow(ctx, query, batch).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(indexed), "import_rows_valid") {
		t.Fatal("worker query did not use the partial index", string(indexed))
	}
	results["withPartialIndex"] = indexed
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DROP INDEX import_rows_valid`); err != nil {
		t.Fatal(err)
	}
	var baseline []byte
	if err = tx.QueryRow(ctx, query, batch).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	results["withoutPartialIndex"] = baseline
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("../../tests/results/import-valid-index.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("50,000 rows, 49,000 already applied; EXPLAIN plans saved to tests/results/import-valid-index.json")
}
