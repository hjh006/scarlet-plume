package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestAPILifecycleInventoryAndVersionChanges(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	want := func(err, expected error) {
		t.Helper()
		if !errors.Is(err, expected) {
			t.Fatalf("got %v, want %v", err, expected)
		}
	}
	want(repo.Migrate(ctx), nil)
	slug := fmt.Sprintf("api-lifecycle-%d", time.Now().UnixNano())
	foreignSlug, limitSlug := slug+"-foreign", slug+"-limit"
	ownerName, viewerName, foreignName := slug+"-owner", slug+"-viewer", slug+"-foreign-owner"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug=ANY($1)", []string{slug, foreignSlug, limitSlug})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username=ANY($1)", []string{ownerName, viewerName, foreignName})
		if err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Lifecycle"), nil)
	want(repo.ProvisionReviewer(ctx, slug, viewerName, "unused"), nil)
	want(repo.Bootstrap(ctx, foreignName, "unused", foreignSlug, "Foreign lifecycle"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	viewer, err := repo.FindUser(ctx, viewerName)
	want(err, nil)
	foreign, err := repo.FindUser(ctx, foreignName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	foreignWorkspace, err := repo.Membership(ctx, foreign.ID, foreignSlug)
	want(err, nil)
	_, err = pool.Exec(ctx, "UPDATE plume.memberships SET role='viewer' WHERE workspace_id=$1 AND user_id=$2", workspace.ID, viewer.ID)
	want(err, nil)
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	importScan := func(source string, operations []apisecurity.Operation) store.APISecurityScan {
		t.Helper()
		input := apisecurity.Scan{Version: "3.1.0", Operations: operations}
		data, err := json.Marshal(input)
		want(err, nil)
		scan, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, source, digest(string(data)), input)
		want(err, nil)
		return scan
	}
	field := func(location, name, category string) apisecurity.SensitiveField {
		return apisecurity.SensitiveField{Location: location, Name: name, Category: category}
	}
	first := importScan("Catalog", []apisecurity.Operation{
		{Method: "GET", Path: "/clients/{id}", Auth: "required", SensitiveFields: []apisecurity.SensitiveField{field("response", "email", "email")}},
		{Method: "POST", Path: "/removed", Auth: "required"},
		{Method: "GET", Path: "/quiet", Auth: "required"},
		{Method: "GET", Path: "/unchanged/{id}", Auth: "required"},
		{Method: "GET", Path: "/aliases/{id}", Auth: "required", SensitiveFields: []apisecurity.SensitiveField{field("request", "phone", "phone")}},
		{Method: "GET", Path: "/aliases/{alias_id}", Auth: "required", SensitiveFields: []apisecurity.SensitiveField{field("request", "phone", "phone")}},
	})
	current := importScan("Catalog", []apisecurity.Operation{
		{Method: "GET", Path: "/clients/{customer_id}", Auth: "anonymous", Deprecated: true, SensitiveFields: []apisecurity.SensitiveField{field("request", "phone", "phone")}},
		{Method: "POST", Path: "/added", Auth: "required"},
		{Method: "GET", Path: "/quiet", Auth: "required"},
		{Method: "GET", Path: "/unchanged/{customer_id}", Auth: "required"},
		{Method: "GET", Path: "/aliases/{alias_id}", Auth: "required", SensitiveFields: []apisecurity.SensitiveField{field("request", "phone", "phone"), field("request", "phone", "phone")}},
		{Method: "GET", Path: "/aliases/{id}", Auth: "required", SensitiveFields: []apisecurity.SensitiveField{field("request", "phone", "phone")}},
	})
	initialOnly := importScan("Baseline only", []apisecurity.Operation{{Method: "GET", Path: "/ready", Auth: "required"}})
	newSource := func(name, service, kind string) (store.RuntimeSource, string) {
		t.Helper()
		token := digest(slug + name)
		source, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, name, service, kind, token)
		want(err, nil)
		return source, token
	}
	gateway, gatewayToken := newSource("Gateway", "Catalog", "gateway")
	probe, probeToken := newSource("Probe", "Catalog", "otlp")
	other, otherToken := newSource("No contract", "Other", "gateway")
	foreignSource, err := repo.CreateRuntimeSource(ctx, foreignWorkspace.ID, foreign.ID, "Foreign source", "Foreign service", "gateway", digest(slug+"-foreign-token"))
	want(err, nil)
	now := time.Now().UTC()
	observations := []runtimesecurity.Observation{
		{Method: "GET", Route: "/clients/{id}", Status: 200, ObservedAt: now},
		{Method: "GET", Route: "/shadow", Status: 200, ObservedAt: now},
		{Method: "POST", Route: "/removed", Status: 404, ObservedAt: now},
	}
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, digest("lifecycle-gateway"), observations)
	want(err, nil)
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, digest("lifecycle-gateway"), observations)
	want(err, nil)
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", probeToken, digest("lifecycle-probe"), observations[:1])
	want(err, nil)
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", otherToken, digest("lifecycle-other"), []runtimesecurity.Observation{{Method: "GET", Route: "/outside", Status: 200, ObservedAt: now}})
	want(err, nil)
	want(repo.RevokeRuntimeSource(ctx, workspace.ID, owner.ID, probe.ID), nil)
	page, err := repo.APILifecycle(ctx, workspace.ID, viewer.ID, store.APILifecycleFilter{})
	want(err, nil)
	if len(page.Items) != 9 || len(page.Sources) != 3 || len(page.Comparisons) != 2 {
		t.Fatalf("unexpected unified scope: %d items, %d sources, %d comparisons", len(page.Items), len(page.Sources), len(page.Comparisons))
	}
	find := func(service, method, key string) store.APILifecycleItem {
		t.Helper()
		for _, item := range page.Items {
			if item.Service == service && item.Method == method && item.RouteKey == key {
				return item
			}
		}
		t.Fatalf("missing %s %s %s", service, method, key)
		return store.APILifecycleItem{}
	}
	client := find("Catalog", "GET", "/clients/{}")
	if client.State != "registered" || !client.Registered || !client.Observed || !client.Deprecated || !slices.Equal(client.Auth, []string{"anonymous"}) || client.ScanID != current.ID || len(client.Sources) != 2 || len(client.AssetIDs) < 2 || len(client.CheckIDs) == 0 {
		t.Fatalf("current contract/observations/links did not reconcile: %+v", client)
	}
	for _, source := range client.Sources {
		if source.Requests != 1 {
			t.Fatal("replayed batches or independent sources were merged into inflated request totals")
		}
	}
	if client.Sources[1].ID != probe.ID || client.Sources[1].RevokedAt == nil {
		t.Fatal("revocation hid or relabelled historical observations")
	}
	if item := find("Catalog", "POST", "/removed"); item.State != "retired_candidate" || item.Registered || item.PreviousScanID != first.ID || !item.Observed || len(item.AssetIDs) == 0 {
		t.Fatal("removed operation lost its historical contract context")
	}
	if item := find("Catalog", "GET", "/shadow"); item.State != "observed" || item.Registered || item.ScanID != current.ID || len(item.CheckIDs) == 0 {
		t.Fatal("unregistered observation lost its baseline or actionable finding")
	}
	if item := find("Catalog", "GET", "/quiet"); item.State != "unobserved" || item.Observed || item.LastSeen != nil {
		t.Fatal("unobserved contract operation was treated as an observed or retired endpoint")
	}
	if item := find("Other", "GET", "/outside"); item.ScanID != 0 || item.State != "observed" {
		t.Fatal("source without a contract borrowed another service's baseline")
	}
	if aliases := find("Catalog", "GET", "/aliases/{}"); len(aliases.Paths) != 2 || len(aliases.SensitiveFields) != 1 {
		t.Fatal("parameter variants or field sets were not canonicalized")
	}
	for _, comparison := range page.Comparisons {
		if comparison.Service == "Baseline only" {
			if !comparison.Initial || comparison.ScanID != initialOnly.ID || len(comparison.Changes) != 0 {
				t.Fatal("first contract was reported as a version delta")
			}
			continue
		}
		if comparison.PreviousScanID != first.ID || comparison.ScanID != current.ID || comparison.Initial || len(comparison.Changes) != 3 {
			t.Fatalf("unexpected preceding-version comparison: %+v", comparison)
		}
		for _, change := range comparison.Changes {
			if change.RouteKey != "/clients/{}" {
				continue
			}
			if !slices.Equal(change.Kinds, []string{"auth_changed", "deprecation_changed", "sensitive_fields_changed"}) || change.Before == nil || change.After == nil || change.Before.SensitiveFields[0].Location != "response" || change.After.SensitiveFields[0].Location != "request" || len(change.Before.AssetIDs) == 0 || len(change.After.AssetIDs) == 0 {
				t.Fatalf("direction/category/version-link change was lost: %+v", change)
			}
		}
	}
	filtered, err := repo.APILifecycle(ctx, workspace.ID, viewer.ID, store.APILifecycleFilter{SourceID: probe.ID})
	want(err, nil)
	if len(filtered.Items) != 5 || len(filtered.Comparisons) != 1 || len(filtered.Items[0].Sources) > 1 {
		t.Fatalf("source filtering escaped its service: %+v", filtered.Items)
	}
	for _, item := range filtered.Items {
		for _, source := range item.Sources {
			if source.ID != probe.ID {
				t.Fatal("runtime source filter leaked another observation source")
			}
		}
	}
	_, err = repo.APILifecycle(ctx, workspace.ID, viewer.ID, store.APILifecycleFilter{Service: "Other", SourceID: gateway.ID})
	want(err, store.ErrInvalid)
	_, err = repo.APILifecycle(ctx, workspace.ID, viewer.ID, store.APILifecycleFilter{SourceID: foreignSource.ID})
	want(err, store.ErrNotFound)
	_, err = repo.APILifecycle(ctx, workspace.ID, foreign.ID, store.APILifecycleFilter{})
	want(err, store.ErrNotFound)
	_, err = repo.APILifecycle(ctx, foreignWorkspace.ID, viewer.ID, store.APILifecycleFilter{})
	want(err, store.ErrNotFound)
	_, err = pool.Exec(ctx, "DELETE FROM plume.runtime_request_audit WHERE source_id=ANY($1)", []int64{gateway.ID, probe.ID, other.ID})
	want(err, nil)
	_, err = pool.Exec(ctx, "UPDATE plume.runtime_security_sources SET audit_count=0 WHERE workspace_id=$1", workspace.ID)
	want(err, nil)
	after, err := repo.APILifecycle(ctx, workspace.ID, viewer.ID, store.APILifecycleFilter{})
	want(err, nil)
	if !reflect.DeepEqual(page.Items, after.Items) || !reflect.DeepEqual(page.Comparisons, after.Comparisons) {
		t.Fatal("audit retention changed lifecycle assertions")
	}
	historical, err := repo.APISecurityScan(ctx, workspace.ID, first.ID)
	want(err, nil)
	if !reflect.DeepEqual(historical, first) {
		t.Fatal("lifecycle normalization mutated the immutable contract snapshot")
	}
	encoded, err := json.Marshal(page)
	want(err, nil)
	if strings.Contains(string(encoded), foreignSource.Name) || strings.Contains(string(encoded), gatewayToken) || strings.Contains(string(encoded), probeToken) {
		t.Fatal("lifecycle export exposed another tenant or source credentials")
	}

	// Fail explicitly beyond the supported service scope; narrowing the same
	// query still works, rather than returning an incomplete directory.
	want(repo.Bootstrap(ctx, ownerName, "unused", limitSlug, "Limit lifecycle"), nil)
	limitWorkspace, err := repo.Membership(ctx, owner.ID, limitSlug)
	want(err, nil)
	for i := 0; i < 51; i++ {
		_, err = repo.ImportAPISecurityScan(ctx, limitWorkspace.ID, owner.ID, fmt.Sprintf("Service-%02d", i), digest(fmt.Sprint(i)), apisecurity.Scan{Version: "3.1.0", Operations: []apisecurity.Operation{{Method: "GET", Path: "/ready", Auth: "required"}}})
		want(err, nil)
	}
	_, err = repo.APILifecycle(ctx, limitWorkspace.ID, owner.ID, store.APILifecycleFilter{})
	want(err, store.ErrAPILifecycleScope)
	narrowed, err := repo.APILifecycle(ctx, limitWorkspace.ID, owner.ID, store.APILifecycleFilter{Service: "Service-00"})
	want(err, nil)
	if len(narrowed.Items) != 1 {
		t.Fatal("a service filter could not narrow an oversized workspace")
	}
}
