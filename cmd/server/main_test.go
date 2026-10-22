package main

import "testing"

func TestHealthURLConfiguredBind(t *testing.T) {
	for addr, want := range map[string]string{"": "http://127.0.0.1:8765/healthz", "0.0.0.0:8765": "http://127.0.0.1:8765/healthz", "127.0.0.1:18765": "http://127.0.0.1:18765/healthz", "[::]:18765": "http://[::1]:18765/healthz"} {
		if got := healthURL(addr); got != want {
			t.Fatalf("%s: %s != %s", addr, got, want)
		}
	}
}
