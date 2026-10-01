package main

import (
	"io"
	"net/http/httptest"
	"testing"
)

func TestHandlerServesGreetingAndVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	handler("hi there", "v9").ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	body, _ := io.ReadAll(rec.Body)
	if got, want := string(body), "hi there (hello v9)\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
