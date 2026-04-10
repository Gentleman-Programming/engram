package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func main() {
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".engram", "engram.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM observations WHERE simhash IS NULL OR simhash = 0").Scan(&count)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Observations without simhash: %d\n", count)
}
