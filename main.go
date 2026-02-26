package main

import (
	"log"

	"github.com/mrtrkmn/orchestrator/cmd"
)

func main() {
	if err := cmd.RootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
