// Command agent is the workload-cluster component. It only reports its version.
package main

import (
	"fmt"
	"os"
	"time"
)

var version string

func main() {
	for {
		fmt.Printf("agent version=%s cluster=%s\n", version, os.Getenv("CLUSTER_NAME"))
		time.Sleep(30 * time.Second)
	}
}
