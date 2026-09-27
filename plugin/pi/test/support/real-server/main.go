// Test-only HTTP bridge for the Pi native-tool persistence contract.
package main

import (
	"fmt"
	"io"
	"net/http/httptest"
	"os"

	"github.com/Gentleman-Programming/engram/v2/internal/server"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func main() {
	if len(os.Args) != 2 {
		panic("expected isolated data directory")
	}
	db, err := store.New(store.FallbackConfig(os.Args[1]))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	srv := server.New(db, 0)
	defer srv.Close()
	http := httptest.NewServer(srv.Handler())
	defer http.Close()
	fmt.Println(http.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
