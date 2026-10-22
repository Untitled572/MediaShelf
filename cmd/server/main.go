package main

import (
	"log"
	"mediashelf/internal/server"
	"os"
)

func main() {
	if err := server.Run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
