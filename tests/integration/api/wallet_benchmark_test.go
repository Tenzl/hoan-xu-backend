package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestWalletContentionBenchmark(t *testing.T) {
	if os.Getenv("RUN_WALLET_BENCHMARK") != "1" {
		t.Skip("explicit benchmark run")
	}
	results := []map[string]any{}
	for _, workers := range []int{10, 100} {
		s, _, _ := testStore(t)
		ctx := context.Background()
		users := make([]string, workers)
		for i := range users {
			tx, err := s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = tx.QueryRow(ctx, `INSERT INTO users(name,role) VALUES('Benchmark customer','customer') RETURNING id::text`).Scan(&users[i])
			if err == nil {
				err = platform.Accounts(ctx, tx, users[i])
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		durations := make(chan float64, workers*10)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i, user := range users {
			wg.Add(1)
			go func(i int, user string) {
				defer wg.Done()
				<-start
				for n := 0; n < 10; n++ {
					at := time.Now()
					tx, err := s.Pool.Begin(ctx)
					if err != nil {
						errs <- err
						return
					}
					err = wallet.Credit(ctx, tx, user, fmt.Sprintf("bench:%d:%d", i, n), "Benchmark", 100)
					if err == nil {
						err = tx.Commit(ctx)
					} else {
						_ = tx.Rollback(ctx)
					}
					if err != nil {
						errs <- err
						return
					}
					durations <- float64(time.Since(at).Microseconds()) / 1000
				}
			}(i, user)
		}
		at := time.Now()
		close(start)
		wg.Wait()
		elapsed := time.Since(at)
		close(durations)
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		samples := []float64{}
		for d := range durations {
			samples = append(samples, d)
		}
		sort.Float64s(samples)
		if n, err := wallet.FullReconcile(ctx, s); err != nil || n != 0 {
			t.Fatal("ledger mismatch", n, err)
		}
		var wrong int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM wallet_accounts WHERE user_id=ANY($1::uuid[]) AND kind='available' AND balance<>1000`, users).Scan(&wrong); err != nil || wrong != 0 {
			t.Fatal(wrong, err)
		}
		result := map[string]any{"workers": workers, "operations": len(samples), "poolMaxConnections": s.Pool.Config().MaxConns, "durationMs": elapsed.Milliseconds(), "operationsPerSecond": float64(len(samples)) / elapsed.Seconds(), "p50Ms": samples[len(samples)/2], "p95Ms": samples[(len(samples)-1)*95/100], "p99Ms": samples[(len(samples)-1)*99/100], "ledgerMismatches": 0}
		results = append(results, result)
		t.Log(result)
	}
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("../../tests/results/wallet-contention.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
}
