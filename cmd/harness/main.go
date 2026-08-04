// Command harness is a minimal, real-world usable agent harness CLI
// modeled after the OpenAI Codex CLI architecture.
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("harness %s\n", version)
		return
	}
	fmt.Println("harness: minimal agent harness (under construction)")
	fmt.Println("usage: harness run <prompt> | harness resume [<session-id>] | harness version")
}
