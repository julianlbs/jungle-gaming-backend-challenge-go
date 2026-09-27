package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
)

func runMigrate(args []string) int {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	url := os.Getenv("MIGRATIONS_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		log.Error("MIGRATIONS_DATABASE_URL or DATABASE_URL must be set")
		return 2
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}

	m, err := postgres.NewMigrator(url)
	if err != nil {
		log.Error("migrator initialization failed", "error", err)
		return 1
	}
	defer func() {
		if err := m.Close(); err != nil {
			log.Warn("closing migrator", "error", err)
		}
	}()

	switch args[0] {
	case "up":
		err = m.Up()
	case "down":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		steps := 0
		if args[1] != "all" {
			steps, err = strconv.Atoi(args[1])
			if err != nil || steps <= 0 {
				fmt.Fprintln(os.Stderr, usage)
				return 2
			}
		}
		err = m.Down(steps)
	case "version":
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if err != nil {
		log.Error("migration failed", "command", args[0], "error", err)
		return 1
	}

	version, dirty, err := m.Version()
	if err != nil {
		log.Error("reading schema version", "error", err)
		return 1
	}
	log.Info("schema version", "command", args[0], "version", version, "dirty", dirty)
	if dirty {
		return 1
	}
	return 0
}
