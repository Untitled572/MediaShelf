package main

import (
	"log"
	"mediashelf/internal/server"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		c := http.Client{Timeout: 3 * time.Second}
		r, e := c.Get(healthURL(os.Getenv("MEDIASHELF_ADDR")))
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	if err := server.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func healthURL(addr string) string {
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:8765/healthz"
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}
