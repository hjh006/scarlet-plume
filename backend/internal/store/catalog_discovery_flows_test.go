package store

import "testing"

func TestCatalogFlowIdentityIncludesDatabaseSystem(t *testing.T) {
	mysql := catalogFlowTargetKey("mysql", "shared", "db", "db", "table")
	if mysql != catalogFlowTargetKey("mysql", "shared", "db", "", "table") {
		t.Fatal("MySQL database/schema namespace alias not canonical")
	}
	if mysql == catalogFlowTargetKey("mariadb", "shared", "db", "db", "table") || mysql == catalogFlowTargetKey("postgresql", "shared", "db", "db", "table") {
		t.Fatal("different database systems collapsed")
	}
	if catalogFlowTargetKey("microsoft.sql_server", "shared", "db", "dbo", "table") != catalogFlowTargetKey("mssql", "shared", "db", "dbo", "table") {
		t.Fatal("SQL Server names not canonical")
	}
}
