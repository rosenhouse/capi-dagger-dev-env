// Command manager stands in for a Go controller.
package main

import (
	"fmt"
	"log"
	"net/http"
)

var version = "dev"

func main() {
	log.Printf("manager version=%s", version)
	log.Fatal(http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, version)
	})))
}
