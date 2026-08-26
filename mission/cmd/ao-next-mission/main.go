package main

import (
	"os"

	"github.com/uesugitorachiyo/ao-mission/internal/mission"
)

func main() { os.Exit(mission.Run(os.Args[1:], os.Stdout, os.Stderr)) }
