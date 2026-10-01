// Command hello serves its build version over HTTP.
package main

import (
	"fmt"
	"log"
	"net/http"
)

var version = "dev"

func main() {
	log.Fatal(http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, version)
	})))
}
