package queue

import (
	"database/sql"
	"fmt"
	"strings"
)

// migrateExternalID adds the optional external_id column and its index.
// Existing rows keep an empty id. The index supports exact-match filters.
func migrateExternalID(db *sql.DB) error {
	exists, err := jobsColumnExists(db, "external_id")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec(`ALTER TABLE jobs ADD COLUMN external_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add jobs.external_id: %w", err)
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS jobs_external_id_idx ON jobs (external_id)`); err != nil {
		return fmt.Errorf("index jobs.external_id: %w", err)
	}
	return nil
}

func jobsColumnExists(db *sql.DB, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

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
