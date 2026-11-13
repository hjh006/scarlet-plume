package collector

import (
	"crypto/tls"
	"github.com/jackc/pgx/v5"
	"net"
)

// PostgreSQLTransport observes the established collector connection, not its
// requested SSL configuration or the database's storage encryption state.
type PostgreSQLTransport struct {
	TLS      bool   `json:"tls"`
	Protocol string `json:"protocol,omitempty"`
	Cipher   string `json:"cipher,omitempty"`
	Evidence string `json:"evidence"`
	Scope    string `json:"scope"`
}

func observePostgreSQLTransport(tx pgx.Tx) *PostgreSQLTransport {
	if tx == nil || tx.Conn() == nil {
		return nil
	}
	connection := tx.Conn().PgConn().Conn()
	result := &PostgreSQLTransport{Evidence: "pgconn_connection_state", Scope: "collector_to_configured_postgres_endpoint"}
	if secured, ok := connection.(interface{ ConnectionState() tls.ConnectionState }); ok {
		state := secured.ConnectionState()
		if !state.HandshakeComplete {
			return nil
		}
		result.TLS = true
		result.Protocol = tls.VersionName(state.Version)
		result.Cipher = tls.CipherSuiteName(state.CipherSuite)
		return result
	}
	switch connection.(type) {
	case *net.TCPConn, *net.UnixConn:
		return result
	default:
		return nil
	}
}
func ValidPostgreSQLTransport(t *PostgreSQLTransport) bool {
	if t == nil {
		return true
	}
	if t.Evidence != "pgconn_connection_state" || t.Scope != "collector_to_configured_postgres_endpoint" {
		return false
	}
	if !t.TLS {
		return t.Protocol == "" && t.Cipher == ""
	}
	if t.Protocol != "TLS 1.2" && t.Protocol != "TLS 1.3" {
		return false
	}
	for _, suite := range append(tls.CipherSuites(), tls.InsecureCipherSuites()...) {
		if suite.Name == t.Cipher {
			return true
		}
	}
	return false
}
