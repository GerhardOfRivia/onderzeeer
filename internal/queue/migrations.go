package queue

import (
	"database/sql"
	"fmt"
	"strings"
)

// SQLite cannot alter CHECK constraints. Rebuild only the job/run tables,
// preserving IDs, history references, and AUTOINCREMENT high-water marks.
func migratePendingState(db *sql.DB) error {
	var definition string
	if err := db.QueryRow("SELECT sql FROM sqlite_schema WHERE name = 'jobs'").Scan(&definition); err != nil {
		return err
	}
	if strings.Contains(definition, "'PENDING'") {
		return nil
	}
	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer db.Exec("PRAGMA foreign_keys = ON")
	// Serialize upgrades from concurrent openers before inspecting the schema.
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer db.Exec("ROLLBACK")
	if err := db.QueryRow("SELECT sql FROM sqlite_schema WHERE name = 'jobs'").Scan(&definition); err != nil {
		return err
	}
	if !strings.Contains(definition, "'PENDING'") {
		for _, table := range []struct{ name, schema string }{{"jobs", jobsSchema}, {"runs", runsSchema}} {
			var sequence int64
			if err := db.QueryRow("SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = ?), 0)", table.name).Scan(&sequence); err != nil {
				return err
			}
			temporary := table.name + "_pending_upgrade"
			create := strings.Replace(table.schema, "IF NOT EXISTS "+table.name, temporary, 1)
			for _, statement := range []string{
				create,
				"INSERT INTO " + temporary + " SELECT * FROM " + table.name,
				"DROP TABLE " + table.name,
				"ALTER TABLE " + temporary + " RENAME TO " + table.name,
			} {
				if _, err := db.Exec(statement); err != nil {
					return fmt.Errorf("rebuild %s: %w", table.name, err)
				}
			}
			if _, err := db.Exec("UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = ?", sequence, table.name); err != nil {
				return err
			}
		}
		if _, err := db.Exec(schema); err != nil {
			return err
		}
		rows, err := db.Query("PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		hasViolation := rows.Next()
		checkErr := rows.Err()
		rows.Close()
		if checkErr != nil {
			return checkErr
		}
		if hasViolation {
			return fmt.Errorf("pending-state upgrade violated history references")
		}
	}
	_, err := db.Exec("COMMIT")
	return err
}
