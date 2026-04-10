package main

import (
	"fmt"
	"log"
	"path/filepath"
	"os"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func main() {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".engram")
	cfg := store.FallbackConfig(dataDir)
	s, err := store.New(cfg)
	if err != nil {
		log.Fatalf("engram: open store: %v", err)
	}
	defer s.Close()

	fmt.Println("Running full re-index of TurboQuant...")
	count, err := s.ReindexTurboQuant()
	if err != nil {
		log.Fatalf("engram: reindex failed: %v", err)
	}
	fmt.Printf("Successfully re-indexed %d observations.\n", count)
}
