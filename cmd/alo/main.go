package main

import (
	"os"

	"alo"
)

func main() {
	os.Exit(alo.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
