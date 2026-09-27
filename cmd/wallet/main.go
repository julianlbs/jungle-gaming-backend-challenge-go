package main

import (
	"fmt"
	"os"

	"go.uber.org/fx"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/config"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/wiring"
)

const usage = `usage:
  wallet serve                run the roles listed in APP_ROLES
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
	case "serve":
		return serve()
	case "migrate":
		return runMigrate(args[1:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
}

func serve() int {
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	app := fx.New(wiring.Options(cfg))
	if err := app.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	app.Run()
	return 0
}
