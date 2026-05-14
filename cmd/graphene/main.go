package main

import (
	"os"

	"github.com/alexghr/graphene/internal/graphene"
)

func main() {
	app := graphene.NewApp("", os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	os.Exit(app.Run(os.Args))
}
