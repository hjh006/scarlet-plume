package store

import "testing"

func TestLocalDockerDatabase(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	const dsn = "postgresql://user:password@db:5432/scarlet?sslmode=disable"
	if _, err := LocalDockerPostgresConfig(dsn); err != nil {
		t.Fatalf("local Docker database rejected: %v", err)
	}
	if _, err := PostgresConfig(dsn, true); err == nil {
		t.Fatal("loopback option accepted a Docker service host")
	}
	for _, dsn := range []string{
		"postgresql://user:password@external.example:5432/scarlet?sslmode=disable",
		"postgresql://user:password@db:5433/scarlet?sslmode=disable",
		"postgresql://user:password@db:5432/scarlet?sslmode=require",
	} {
		if _, err := LocalDockerPostgresConfig(dsn); err == nil {
			t.Fatal("Docker option accepted an unsafe database configuration")
		}
	}
}
