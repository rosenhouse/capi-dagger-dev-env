// Command manager stands in for a management-cluster addon controller and serves its build version.
package main

import (
	"fmt"
	"log"
	"net/http"
)

var version = "dev"

func main() {
	log.Printf("manager %s", version)
	log.Fatal(http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, version)
	})))
}
