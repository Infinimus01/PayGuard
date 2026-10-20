// Command payguard serves the v1 risk API.
// PAYGUARD_API_KEYS is "key:tenant,key2:tenant2".
// DATABASE_URL is a Postgres DSN. If unset, decisions stay in memory.
// PAYGUARD_ADDR defaults to :8080.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Infinimus01/PayGuard/internal/api"
	"github.com/Infinimus01/PayGuard/internal/store"
	"github.com/Infinimus01/PayGuard/internal/tenant"
)

func main() {
	spec := os.Getenv("PAYGUARD_API_KEYS")
	if spec == "" {
		log.Fatal("PAYGUARD_API_KEYS is required, format key:tenant,key2:tenant2")
	}
	keys := tenant.Parse(spec)

	var st store.Store = store.NewMemory()
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pg, err := store.OpenPostgres(ctx, dsn)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		defer pg.Close()
		st = pg
		log.Print("store: postgres")
	} else {
		log.Print("DATABASE_URL unset, store: memory")
	}

	addr := os.Getenv("PAYGUARD_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, api.New(keys, st).Handler()); err != nil {
		log.Fatal(err)
	}
}
