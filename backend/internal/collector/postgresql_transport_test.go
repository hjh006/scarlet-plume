package collector

import (
	"os"
	"testing"
)

func TestPostgreSQLTransportLegacyUnknown(t *testing.T) {
	if observePostgreSQLTransport(nil) != nil || !ValidPostgreSQLTransport(nil) {
		t.Fatal("legacy transport must remain unknown")
	}
	if ValidPostgreSQLTransport(&PostgreSQLTransport{TLS: true, Protocol: "TLS 1.3", Cipher: "not-observed", Evidence: "ssl_configuration", Scope: "collector_to_configured_postgres_endpoint"}) {
		t.Fatal("configuration promoted to observed connection state")
	}
	if ValidPostgreSQLTransport(&PostgreSQLTransport{TLS: true, Protocol: "TLS 1.3", Cipher: "not-a-cipher", Evidence: "pgconn_connection_state", Scope: "collector_to_configured_postgres_endpoint"}) {
		t.Fatal("unrecognized cipher metadata accepted")
	}
}
func TestPostgreSQLTransportActualTLSAndPlaintext(t *testing.T) {
	secure, plain := os.Getenv("SCARLET_TRANSPORT_TLS_TEST_DSN"), os.Getenv("SCARLET_TRANSPORT_PLAIN_TEST_DSN")
	if secure == "" || plain == "" {
		t.Skip("requires isolated verified-TLS and explicit loopback plaintext fixtures")
	}
	for _, tc := range []struct {
		name, dsn string
		wantTLS   bool
		insecure  bool
	}{{"verified_tls", secure, true, false}, {"explicit_loopback_plaintext", plain, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			tables, e := InspectPostgreSQL(t.Context(), tc.dsn, []string{"transport_fixture"}, tc.insecure)
			if e != nil {
				t.Fatal(e)
			}
			if len(tables) != 1 || tables[0].Transport == nil {
				t.Fatal("actual transport evidence missing")
			}
			observed := tables[0].Transport
			if !ValidPostgreSQLTransport(observed) || observed.TLS != tc.wantTLS {
				t.Fatalf("incorrect actual connection evidence: %+v", observed)
			}
			if tc.wantTLS && (observed.Protocol == "" || observed.Cipher == "") {
				t.Fatal("TLS protocol/cipher missing")
			}
			if !tc.wantTLS && (observed.Protocol != "" || observed.Cipher != "") {
				t.Fatal("plaintext reported TLS protocol/cipher")
			}
			t.Logf("actual collector connection TLS=%t protocol=%s cipher=%s evidence=%s scope=%s", observed.TLS, observed.Protocol, observed.Cipher, observed.Evidence, observed.Scope)
		})
	}
}
