// Standalone stdlib-only synthetic CLI/transport fixture, not production handlers.
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

func record(value any) {
	file, err := os.OpenFile(os.Getenv("ENGRAM_PATH_TEST_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer func() { _ = file.Close() }()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		panic(err)
	}
}

func main() {
	record(map[string]any{"args": os.Args[1:]})
	if len(os.Args) == 2 && os.Args[1] == "instance-id" {
		_, _ = os.Stdout.WriteString("00000000000000000000000000000000\n")
		return
	}
	if len(os.Args) != 2 || os.Args[1] != "serve" {
		os.Exit(2)
	}
	server := &http.Server{Addr: "127.0.0.1:" + os.Getenv("ENGRAM_PORT"), ReadHeaderTimeout: time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(map[string]any{"method": r.Method, "path": r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/runtime-contract":
			// The test supplies the complete current Go-generated projection plus instance.
			payload, err := os.ReadFile(os.Getenv("ENGRAM_PATH_TEST_CONTRACT"))
			if err != nil {
				panic(err)
			}
			_, _ = w.Write(payload)
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok","service":"engram","instance_id":"00000000000000000000000000000000","capabilities":{"isolated_session_registration":true}}`))
		case "/project/current":
			_, _ = w.Write([]byte(`{"project":"path-project"}`))
		case "/sessions":
			var body struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": body.ID, "status": "created"})
		case "/observations":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":1}`))
		case "/fixture-stop":
			_, _ = w.Write([]byte(`{}`))
			go func() { time.Sleep(10 * time.Millisecond); _ = server.Close() }()
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	// A failed assertion cannot leave the detached fixture indefinitely alive.
	timer := time.AfterFunc(20*time.Second, func() { _ = server.Close() })
	defer timer.Stop()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
