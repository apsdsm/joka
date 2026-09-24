package connection

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/apsdsm/joka/config"
	"github.com/apsdsm/joka/internal/providers"
)

// defaultProvider is what a config means when it does not say. AWS is the only
// one joka ships, and every config written before there was a choice meant it.
const defaultProvider = "aws"

// openTunnel starts the port forward a connection declares.
//
// A connection with no tunnel returns a nil session and a closer that does
// nothing, so no caller branches on whether there is one.
func openTunnel(ctx context.Context, conn *config.Connection, dsn string) (*providers.Session, func() error, error) {
	noop := func() error { return nil }

	if conn == nil || conn.Tunnel == nil {
		return nil, noop, nil
	}

	t := conn.Tunnel

	name := t.Provider
	if name == "" {
		name = defaultProvider
	}

	provider, err := providers.LookupTunnel(name)
	if err != nil {
		return nil, noop, err
	}

	// The database is named once. Left to be repeated under `tunnel:`, the two
	// would be edited separately and eventually disagree — and the failure,
	// connecting to the wrong database through a working tunnel, is silent.
	host, port := t.RemoteHost, t.RemotePort
	if host == "" {
		host = conn.Host
	}
	if port == 0 {
		port = conn.Port
	}

	// Then whatever the DSN says. With source: env the connection block has no
	// host at all - DATABASE_URL holds the only copy - and making the author
	// write it out again under `tunnel:` is asking for the two to disagree.
	if dsnHost, dsnPort, ok := hostPortOf(dsn); ok {
		if host == "" {
			host = dsnHost
		}
		if port == 0 {
			port = dsnPort
		}
	}

	if port == 0 {
		port = 5432
	}
	if host == "" {
		return nil, noop, fmt.Errorf("the tunnel has no remote host: set tunnel.remote_host, connection.host, or a host in the DSN")
	}

	session, err := provider.Open(ctx, providers.TunnelSpec{
		Target:     t.Target,
		RemoteHost: host,
		RemotePort: port,
		LocalPort:  t.LocalPort,
		Params:     t.Params,
	})
	if err != nil {
		return nil, noop, fmt.Errorf("opening the %s tunnel: %w", name, err)
	}

	return session, session.Close, nil
}

// redirectDSN points a DSN at the tunnel's local end.
//
// The rewrite is on the finished DSN rather than on the connection fields
// because the DSN does not always come from them: a secret in whole-URL mode
// carries the real host inside the URL, and a `url:` under source literal does
// the same. Rewriting the config would leave both of those talking to a
// database the tunnel is not forwarding to, which fails by connecting
// somewhere real rather than by failing.
func redirectDSN(dsn string, session *providers.Session) (string, error) {
	if session == nil {
		return dsn, nil
	}

	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("rewriting the DSN for the tunnel: %w", err)
	}

	u.Host = net.JoinHostPort(session.LocalHost, strconv.Itoa(session.LocalPort))

	return u.String(), nil
}

// hostPortOf reads the host and port out of a DSN, so a tunnel can default to
// the database the connection already names.
func hostPortOf(dsn string) (string, int, bool) {
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() == "" {
		return "", 0, false
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		port = 0
	}

	return u.Hostname(), port, true
}
