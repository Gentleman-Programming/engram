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

	obs, err := s.AllObservations("", "", 50)
	if err != nil {
		log.Fatalf("engram: fetch obs: %v", err)
	}

	fmt.Printf("Total observations found: %d\n", len(obs))
	fmt.Println("ID | Title | SimHash")
	fmt.Println("-----------------------")
	missing := 0
	for _, o := range obs {
		fmt.Printf("%d | %s | %d\n", o.ID, o.Title, o.SimHash)
		if o.SimHash == 0 {
			missing++
		}
	}
	fmt.Printf("\nObservations missing SimHash: %d\n", missing)
}
