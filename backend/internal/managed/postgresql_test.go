package managed

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestManagedPasswordIsBoundToConnection(t *testing.T) {
	manager, err := New(nil, strings.Repeat("a1", 32), "catalog.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	source := Connection{
		Host: "catalog.example.test", Port: 5432, Database: "assets", User: "catalog_reader",
		Schemas: []string{"inventory"},
	}
	const password = "sample:#p?%[\n测试"
	ciphertext, err := manager.EncryptPassword(source, password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.EncryptPassword(source, password)
	if err != nil || slices.Equal(ciphertext, second) || strings.Contains(string(ciphertext), password) {
		t.Fatal("password encryption is not random and opaque")
	}

	source.Schemas = []string{"inventory", "audit"}
	if got, err := manager.decryptPassword(source, ciphertext); err != nil || got != password {
		t.Fatal("schema changes prevented reuse of the stored password")
	}

	source.Port++
	if _, err := manager.decryptPassword(source, ciphertext); err == nil {
		t.Fatal("password could be reused for a changed destination")
	}
	source.Port--
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := manager.decryptPassword(source, ciphertext); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestManagedPostgresLimitsDestinationAndEncodesCredentials(t *testing.T) {
	manager, err := New(nil, strings.Repeat("a1", 32), "db.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	sample := Connection{Host: "sample-db", Port: 5432, Database: "schema/sample", User: "sample:user", Schemas: []string{"public"}}
	if err := manager.Validate(sample); err != nil || !manager.dockerSample(sample) {
		t.Fatal("local Docker sample was rejected")
	}
	const password = "example?@:/#%&"
	dsn, err := url.Parse(postgresURL(sample, password, "disable"))
	if err != nil {
		t.Fatal("invalid encoded PostgreSQL URL")
	}
	if got, ok := dsn.User.Password(); !ok || got != password ||
		dsn.Host != "sample-db:5432" || dsn.User.Username() != sample.User || dsn.Path != "/"+sample.Database ||
		dsn.RawQuery != "sslmode=disable" {
		t.Fatal("connection components were not preserved")
	}

	sample.Port++
	if err := manager.Validate(sample); err == nil || manager.dockerSample(sample) {
		t.Fatal("unexpected plaintext port was accepted")
	}
	sample.Host = "db.example.test"
	if err := manager.Validate(sample); err != nil || manager.dockerSample(sample) {
		t.Fatal("approved remote host did not require TLS")
	}
	sample.Host = "127.0.0.1"
	if err := manager.Validate(sample); err == nil {
		t.Fatal("unapproved loopback host was accepted")
	}
	if got := manager.AllowedHosts(); !slices.Equal(got, []string{"db.example.test", "sample-db"}) {
		t.Fatal("unexpected managed PostgreSQL host list")
	}

	remoteOnly, err := New(nil, strings.Repeat("a1", 32), "", false)
	if err != nil {
		t.Fatal(err)
	}
	sample.Host, sample.Port = "sample-db", 5432
	if err := remoteOnly.Validate(sample); err == nil {
		t.Fatal("sample host was enabled outside local Docker")
	}
}
