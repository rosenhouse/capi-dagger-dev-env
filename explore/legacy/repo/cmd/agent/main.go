// Command agent runs in each workload cluster and reports its build version.
package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/acme/fleet-addons/internal/version"
)

func main() {
	log.Printf("fleet-agent %s", version.Version)
	log.Fatal(http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, version.Version)
	})))
}
