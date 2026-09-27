package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// runHealth probes the local readiness endpoint so that container health checks work in an
// image without a shell or curl.
func runHealth() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid HTTP_ADDR:", err)
		return 2
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/health/ready")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "not ready:", resp.Status)
		return 1
	}
	return 0
}
