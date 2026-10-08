//go:build !generate

package main

import "github.com/swysgh/mill-box/log"

func main() {
	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
