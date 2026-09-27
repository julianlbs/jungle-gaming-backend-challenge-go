package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  wallet migrate up           apply all pending migrations
  wallet migrate down <n|all> revert the last n migrations, or all of them
  wallet migrate version      print the current schema version`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "migrate":
		return runMigrate(args[1:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
}
