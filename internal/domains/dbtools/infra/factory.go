package infra

import (
	"database/sql"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/dbtools/app"
)

// NewDBAdapter returns the appropriate DBAdapter for the given driver.
func NewDBAdapter(driver jokadb.Driver, conn *sql.DB) app.DBAdapter {
	if driver == jokadb.Postgres {
		return NewPostgresDBAdapter(conn)
	}
	return NewMySQLDBAdapter(conn)
}
