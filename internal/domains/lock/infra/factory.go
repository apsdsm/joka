package infra

import (
	"database/sql"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/lock/app"
)

// NewLockAdapter returns the appropriate LockAdapter for the given driver.
func NewLockAdapter(driver jokadb.Driver, conn *sql.DB) app.LockAdapter {
	if driver == jokadb.Postgres {
		return NewPostgresLockAdapter(conn)
	}
	return NewMySQLLockAdapter(conn)
}
