// Package connection resolves a joka Connection config into a database DSN.
//
// Sources:
//   - "env" (default): use DATABASE_URL from the environment. The caller is
//     responsible for having loaded any --env dotenv first.
//   - "aws_secrets_manager": fetch a secret and either use a stored full DSN
//     (whole-URL mode) or assemble a URL-safe DSN from the Connection fields
//     with the password taken from the secret (assembly mode).
package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"

	"github.com/apsdsm/joka/config"
	"github.com/go-sql-driver/mysql"
)

// SecretFetcher fetches a secret by id and returns its values. A secret stored
// as a JSON object yields its fields; a plain-string secret yields a single
// entry under the "" key. Implementations let tests stub out the provider.
type SecretFetcher interface {
	Fetch(ctx context.Context, secretID, region string) (map[string]string, error)
}

// Resolve turns a Connection into a DSN string suitable for db.Open. A nil
// connection or the "env" source uses DATABASE_URL from the environment. When a
// secret-backed source needs a fetcher and none is supplied, the default AWS
// Secrets Manager fetcher is used.
func Resolve(ctx context.Context, conn *config.Connection, fetcher SecretFetcher) (string, error) {
	switch source(conn) {
	case "env":
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return "", fmt.Errorf("DATABASE_URL not found in environment variables")
		}
		return dsn, nil

	case "literal":
		if conn.URL != "" {
			return conn.URL, nil
		}
		return assembleDSN(conn, conn.Password)

	case "aws_secrets_manager":
		if conn.Secret == nil || conn.Secret.SecretID == "" {
			return "", fmt.Errorf("connection source aws_secrets_manager requires secret.secret_id")
		}
		if fetcher == nil {
			fetcher = newAWSSecretsManager()
		}
		values, err := fetcher.Fetch(ctx, conn.Secret.SecretID, conn.Secret.Region)
		if err != nil {
			return "", fmt.Errorf("fetching secret %q: %w", conn.Secret.SecretID, err)
		}
		return dsnFromSecret(conn, values)

	default:
		return "", fmt.Errorf("unknown connection source %q", conn.Source)
	}
}

// source returns the explicit source, or infers one: a secret block implies
// aws_secrets_manager; an inline url/password (or bare connection parts) implies
// literal; otherwise env (DATABASE_URL).
func source(conn *config.Connection) string {
	if conn == nil {
		return "env"
	}
	if conn.Source != "" {
		return conn.Source
	}
	switch {
	case conn.Secret != nil:
		return "aws_secrets_manager"
	case conn.URL != "" || conn.Password != "" || conn.Host != "" || conn.Driver != "":
		return "literal"
	default:
		return "env"
	}
}

// assembleDSN builds a URL-safe DSN from the connection parts and a password.
func assembleDSN(conn *config.Connection, password string) (string, error) {
	driver := conn.Driver
	if driver == "" {
		driver = "mysql"
	}
	switch driver {
	case "mysql":
		return buildMySQLDSN(conn, password), nil
	case "postgres", "postgresql":
		return buildPostgresDSN(conn, password), nil
	default:
		return "", fmt.Errorf("unsupported driver %q for assembled connection", driver)
	}
}

// dsnFromSecret builds the DSN from fetched secret values. Assembly mode is used
// when a password_key or connection parts are configured; otherwise the secret
// is treated as holding a complete DSN (whole-URL mode).
func dsnFromSecret(conn *config.Connection, values map[string]string) (string, error) {
	sec := conn.Secret
	assembly := sec.PasswordKey != "" || conn.Host != "" || conn.Driver != ""

	if assembly {
		password := ""
		if sec.PasswordKey != "" {
			v, ok := values[sec.PasswordKey]
			if !ok {
				return "", fmt.Errorf("secret key %q (password_key) not found in secret", sec.PasswordKey)
			}
			password = v
		}
		return assembleDSN(conn, password)
	}

	// whole-URL mode: a named JSON key, or a plain-string secret (keyed "").
	key := sec.URLKey
	v, ok := values[key]
	if !ok {
		if key == "" {
			return "", fmt.Errorf("secret holds no usable DSN: set secret.url_key, secret.password_key, or store the secret as a plain DSN string")
		}
		return "", fmt.Errorf("secret key %q (url_key) not found in secret", key)
	}
	return v, nil
}

func buildMySQLDSN(conn *config.Connection, password string) string {
	host := conn.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := conn.Port
	if port == 0 {
		port = 3306
	}

	c := mysql.NewConfig()
	c.User = conn.User
	c.Passwd = password
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	c.DBName = conn.Database
	if len(conn.Params) > 0 {
		c.Params = make(map[string]string, len(conn.Params))
		for k, v := range conn.Params {
			c.Params[k] = v
		}
	}
	// FormatDSN/ParseDSN round-trip the password safely, so db.Open's re-parse is fine.
	return c.FormatDSN()
}

func buildPostgresDSN(conn *config.Connection, password string) string {
	host := conn.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := conn.Port
	if port == 0 {
		port = 5432
	}

	u := &url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(conn.User, password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + conn.Database,
	}
	if len(conn.Params) > 0 {
		q := url.Values{}
		for k, v := range conn.Params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// parseSecretString returns a JSON object's fields, or for a non-JSON secret a
// single entry keyed by "".
func parseSecretString(s string) map[string]string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err == nil {
		m := make(map[string]string, len(obj))
		for k, v := range obj {
			m[k] = fmt.Sprintf("%v", v)
		}
		return m
	}
	return map[string]string{"": s}
}
