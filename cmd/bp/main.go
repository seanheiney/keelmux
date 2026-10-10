package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("bp development (protocol bp/1)")
		return
	}
	fmt.Println("Agent Backplane core build. Runtime integration is under construction.")
}
