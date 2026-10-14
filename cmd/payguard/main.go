// Command payguard serves the v1 risk API.
// PAYGUARD_API_KEYS is "key:tenant,key2:tenant2". PAYGUARD_ADDR defaults to :8080.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/Infinimus01/PayGuard/internal/api"
	"github.com/Infinimus01/PayGuard/internal/tenant"
)

func main() {
	spec := os.Getenv("PAYGUARD_API_KEYS")
	if spec == "" {
		log.Fatal("PAYGUARD_API_KEYS is required, format key:tenant,key2:tenant2")
	}
	keys := tenant.Parse(spec)
	addr := os.Getenv("PAYGUARD_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, api.New(keys).Handler()); err != nil {
		log.Fatal(err)
	}
}
