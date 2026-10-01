// Command hello serves the GREETING environment variable over HTTP.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.Fatal(http.ListenAndServe(":8080", handler(os.Getenv("GREETING"), version)))
}

func handler(greeting, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s (hello %s)\n", greeting, version)
	})
}
