package store

import (
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresConfig validates the explicit connection used by the server and maintenance commands.
func PostgresConfig(dsn string, allowLoopback bool) (*pgxpool.Config, error) {
	if dsn == "" {
		return nil, errors.New("SCARLET_DB_DSN is required")
	}
	if os.Getenv("PGSERVICE") != "" {
		return nil, errors.New("use an explicit database DSN without PGSERVICE")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	conn := config.ConnConfig
	if len(conn.Fallbacks) != 0 || conn.Host == "" || strings.HasPrefix(conn.Host, "/") ||
		strings.HasPrefix(conn.Host, "@") || strings.Contains(conn.Host, ",") {
		return nil, errors.New("database connection requires a single TCP host without fallbacks")
	}
	tls := conn.TLSConfig
	ip := net.ParseIP(conn.Host)
	if !(tls != nil && !tls.InsecureSkipVerify && tls.ServerName == conn.Host) &&
		!(allowLoopback && tls == nil && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("database requires sslmode=verify-full; plaintext is limited to explicit loopback development")
	}
	conn.ConnectTimeout = 5 * time.Second
	return config, nil
}
