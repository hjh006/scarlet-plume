package collector

import (
	"errors"
	"net/url"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// DiscoveryConnection stays in the customer process; none of these credentials
// are serialized into platform configuration or collection results.
type DiscoveryConnection struct {
	DSN                   string `json:"-"`
	Token                 string `json:"-"`
	AllowInsecureLoopback bool   `json:"-"`
	LocalDocker           bool   `json:"-"`
}

// ValidateDiscoveryConnection bounds the explicitly selected Docker fixture
// mode before dialing. Production adapters retain their TLS verification path.
func ValidateDiscoveryConnection(c DiscoveryConfig, connection DiscoveryConnection) error {
	if !connection.LocalDocker {
		return nil
	}
	invalid := errors.New("本地Docker发现仅允许固定样例服务、端口和样例数据库")
	switch c.Provider {
	case "mysql", "mariadb":
		cfg, e := mysql.ParseDSN(connection.DSN)
		if e != nil || cfg.Net != "tcp" || cfg.Addr != c.Provider+":3306" || cfg.DBName != "discovery_sample" || c.Database != "discovery_sample" || cfg.MultiStatements || cfg.AllowAllFiles {
			return invalid
		}
	case "mongodb":
		u, e := url.Parse(connection.DSN)
		if e != nil || u.Scheme != "mongodb" || u.Host != "mongodb:27017" || u.Path != "/discovery_sample" || c.Database != "discovery_sample" || u.Fragment != "" {
			return invalid
		}
	case "postgresql":
		u, e := url.Parse(connection.DSN)
		if e != nil || (u.Scheme != "postgresql" && u.Scheme != "postgres") || u.Host != "sample-db:5432" || strings.TrimPrefix(u.Path, "/") != "scarlet_sample" || c.Database != "scarlet_sample" || u.Query().Get("sslmode") != "disable" {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}
