// Command lister writes a heartbeat ConfigMap in its namespace every few seconds, logging failures.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const sa = "/var/run/secrets/kubernetes.io/serviceaccount/"

func main() {
	token, err := os.ReadFile(sa + "token")
	if err != nil {
		log.Fatal(err)
	}
	ns, _ := os.ReadFile(sa + "namespace")
	ca, _ := os.ReadFile(sa + "ca.crt")
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	url := fmt.Sprintf("https://%s/api/v1/namespaces/%s/configmaps",
		net.JoinHostPort(os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")), ns)
	for {
		body := fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"lister-heartbeat"},"data":{"at":%q}}`, time.Now().Format(time.RFC3339))
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+string(token))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("heartbeat: %v", err)
		} else {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			log.Printf("heartbeat: %s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
		time.Sleep(5 * time.Second)
	}
}
