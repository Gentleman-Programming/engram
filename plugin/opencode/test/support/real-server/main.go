// Test-only isolated HTTP bridge for the OpenCode adapter persistence contract.
package main

import (
	"fmt"
	"io"
	"net/http/httptest"
	"os"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func main() {
	if len(os.Args) != 2 {
		panic("expected isolated data directory")
	}
	db, err := store.New(store.FallbackConfig(os.Args[1]))
	if err != nil {
		panic(err)
	}
	defer func() { _ = db.Close() }()
	srv := server.New(db, 0)
	defer func() { _ = srv.Close() }()
	http := httptest.NewServer(srv.Handler())
	defer http.Close()
	fmt.Println(http.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
