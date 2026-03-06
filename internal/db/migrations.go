package db

import (
	"database/sql"
	"fmt"
)

func migrate(d *sql.DB) error {
	migrations, err := migrationsFS.ReadFile("migrations/001_init.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	_, err = d.Exec(string(migrations))
	if err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}
	return nil
}
