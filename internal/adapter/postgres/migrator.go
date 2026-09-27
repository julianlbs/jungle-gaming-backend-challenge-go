package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5 driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/migrations"
)

// Migrator applies and reverts the embedded migrations.
type Migrator struct {
	m *migrate.Migrate
}

func NewMigrator(databaseURL string) (*Migrator, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, driverURL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("init migrator: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Up applies all pending migrations.
func (g *Migrator) Up() error {
	if err := g.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Down reverts the given number of migrations; steps <= 0 reverts all of them.
func (g *Migrator) Down(steps int) error {
	var err error
	if steps <= 0 {
		err = g.m.Down()
	} else {
		err = g.m.Steps(-steps)
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Version returns the current version; dirty means a migration failed midway.
func (g *Migrator) Version() (version uint, dirty bool, err error) {
	version, dirty, err = g.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}

func (g *Migrator) Close() error {
	srcErr, dbErr := g.m.Close()
	return errors.Join(srcErr, dbErr)
}

func driverURL(databaseURL string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if rest, ok := strings.CutPrefix(databaseURL, prefix); ok {
			return "pgx5://" + rest
		}
	}
	return databaseURL
}
