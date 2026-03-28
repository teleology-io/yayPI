package dialect

import "fmt"

// FromDriver returns the Dialect for a given driver string from DBConfig.
// Recognized values: "postgres" (default), "mysql", "sqlite".
func FromDriver(driver string) (Dialect, error) {
	switch driver {
	case "", "postgres", "postgresql":
		return Postgres{}, nil
	case "mysql", "mariadb":
		return MySQL{}, nil
	case "sqlite", "sqlite3":
		return SQLite{}, nil
	default:
		return nil, fmt.Errorf("unsupported database driver %q; supported: postgres, mysql, sqlite", driver)
	}
}
