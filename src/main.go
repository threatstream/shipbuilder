package main

import (
	"log"
	"os"
)

func main() {
	args := os.Args
	if len(args) < 2 {
		log.Fatalln("expected at least one argument")
		return
	}
	switch args[1] {
	case "server":
		sbServer := NewServer()
		log.Println(sbServer.start())
	default:
		new(Client).Do(args)
	}

}
