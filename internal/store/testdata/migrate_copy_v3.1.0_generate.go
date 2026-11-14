package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func main() {
	if len(os.Args) != 3 {
		panic("expected new synthetic directory and catalog output")
	}
	dir := os.Args[1]
	if !filepath.IsAbs(dir) {
		panic("absolute directory required")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		panic(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "engram.db"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if err = f.Close(); err != nil {
		panic(err)
	}
	s, err := store.New(store.FallbackConfig(dir))
	if err != nil {
		panic(err)
	}
	db := s.DB()
	var version int
	var sqlite, integrity string
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		panic(err)
	}
	if err = db.QueryRow("SELECT sqlite_version()").Scan(&sqlite); err != nil {
		panic(err)
	}
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		panic(err)
	}
	rows, err := db.Query("SELECT name,type,tbl_name,ifnull(sql,'') FROM sqlite_schema ORDER BY name")
	if err != nil {
		panic(err)
	}
	type object struct{ Kind, Table, SQL string }
	catalog := map[string]object{}
	counts := map[string]int{}
	for rows.Next() {
		var name string
		var o object
		if err = rows.Scan(&name, &o.Kind, &o.Table, &o.SQL); err != nil {
			panic(err)
		}
		catalog[name] = o
		counts[o.Kind]++
	}
	if err = rows.Err(); err != nil {
		panic(err)
	}
	if err = rows.Close(); err != nil {
		panic(err)
	}
	b, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(os.Args[2], append(b, '\n'), 0600); err != nil {
		panic(err)
	}
	if err = s.Close(); err != nil {
		panic(err)
	}
	fmt.Printf("objects=%d counts=%v user_version=%d sqlite=%s integrity=%s\n", len(catalog), counts, version, sqlite, integrity)
}
