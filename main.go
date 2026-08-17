// Command task042-tarindex runs the tar.gz content index and search service.
//
// Use --smoke-test to run the built-in self-check, which exits the process on
// completion. Otherwise it serves the HTTP API with `server --addr :8080`.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"task042-tarindex/internal/httpapi"
	"task042-tarindex/internal/selfcheck"
	"task042-tarindex/internal/tarindex"
)

func main() {
	args := os.Args[1:]

	// --smoke-test runs the self-check and exits.
	if len(args) > 0 && args[0] == "--smoke-test" {
		if err := selfcheck.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "smoke-test FAILED: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("smoke-test PASSED")
		return
	}

	// Tolerate an optional leading "server" subcommand so flags after it
	// (e.g. `server --addr :9090`) are parsed by the flag set.
	if len(args) > 0 && args[0] == "server" {
		args = args[1:]
	}

	fs := flag.NewFlagSet("tarindex", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "HTTP listen address")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s --smoke-test                 run self-check and exit\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s server --addr :8080          start the HTTP server\n", os.Args[0])
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	svc := tarindex.NewService()
	api := httpapi.New(svc)
	hs := &http.Server{
		Addr:              *addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
	}
	log.Printf("task042-tarindex listening on %s", *addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
