// Command hello serves its build version over HTTP.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

var version = "dev"

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	log.Printf("hello %s listening on %s", version, *addr)
	log.Fatal(http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, version)
	})))
}
