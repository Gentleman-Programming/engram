// engram-cloud is the cloud sync server for multi-user engram memory sharing.
// It provides a PostgreSQL-backed HTTP API for push/pull sync, direct CRUD,
// and project management.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Gentleman-Programming/engram/internal/cloudserver"
	"github.com/Gentleman-Programming/engram/internal/cloudstore"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe()
	case "version":
		fmt.Printf("engram-cloud %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func cmdServe() {
	dbURL := envOr("ENGRAM_CLOUD_DB", "postgres://localhost:5432/engram_cloud?sslmode=disable")
	port := envOr("ENGRAM_CLOUD_PORT", "8080")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := cloudstore.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer store.Close()

	if err := store.RunMigrations(ctx); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}

	handler := cloudserver.New(store)
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: handler,
	}

	// Start server in goroutine
	go func() {
		log.Printf("engram-cloud %s listening on :%s", version, port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("engram-cloud: shutting down")
	if err := srv.Shutdown(context.Background()); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func printUsage() {
	fmt.Println(`engram-cloud — Cloud sync server for engram

Usage:
  engram-cloud serve       Start the HTTP server
  engram-cloud version     Print version
  engram-cloud help        Show this help

Environment:
  ENGRAM_CLOUD_DB    PostgreSQL connection string (default: postgres://localhost:5432/engram_cloud?sslmode=disable)
  ENGRAM_CLOUD_PORT  HTTP port (default: 8080)`)
}
