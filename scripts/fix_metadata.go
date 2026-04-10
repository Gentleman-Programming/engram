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
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	fmt.Println("Fixing missing projects in observations table...")
	
	// Update observations where project is null/empty by joining with sessions
	res, err := db.Exec(`
		UPDATE observations 
		SET project = (SELECT project FROM sessions WHERE sessions.id = observations.session_id)
		WHERE project IS NULL OR project = ''
	`)
	if err != nil {
		log.Fatalf("failed to update projects: %v", err)
	}

	affected, _ := res.RowsAffected()
	fmt.Printf("Populated project field for %d observations.\n", affected)

	// Also ensure all observations have a simhash (safety re-check)
	fmt.Println("Ensuring simhash is populated for all observations...")
	// We'll let the app's Reindex handle the actual hashing if needed, 
	// but here we just check count.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM observations WHERE simhash IS NULL OR simhash = 0").Scan(&count)
	fmt.Printf("Observations still missing simhash: %d\n", count)
}
