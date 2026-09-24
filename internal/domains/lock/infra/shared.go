package infra

import (
	"os"
	"strconv"
)

// lockerIdentity returns a "hostname:pid" string identifying the current
// process. Stored as locked_by so operators can tell which machine and process
// holds the lock.
func lockerIdentity() string {
	hostname, _ := os.Hostname()
	return hostname + ":" + strconv.Itoa(os.Getpid())
}
