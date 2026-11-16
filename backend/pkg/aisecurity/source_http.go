package aisecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// DocumentSource is an authoritative source adapter. It receives authenticated
// identity and selected catalog IDs, never a caller-supplied URL or ACL grant.
type DocumentSource = func(context.Context, Identity, []string) (map[string]LocalDocument, error)

const DocumentSourceProtocolVersion = "scarlet-document-source-v1"

// NewHTTPDocumentSource implements the private source authorization profile in
// docs/ai-security-source-connector.md. Source-side identity binding, current ACL,
// leases and actual content hashes are mandatory; the gateway still intersects
// them with the published catalog. The source must implement its own real ACLs.
func NewHTTPDocumentSource(endpoint, token string, allowLoopback bool) (DocumentSource, error) {
	if !allowLoopback {
		return nil, errors.New("production document source requires a workspace binding")
	}
	return newHTTPDocumentSource(endpoint, token, "", allowLoopback)
}

func NewHTTPDocumentSourceForWorkspace(endpoint, token, workspace string, allowLoopback bool) (DocumentSource, error) {
	if !validID(workspace) {
		return nil, errors.New("document source workspace binding required")
	}
	return newHTTPDocumentSource(endpoint, token, workspace, allowLoopback)
}

func newHTTPDocumentSource(endpoint, token, workspace string, allowLoopback bool) (DocumentSource, error) {
	if !validTarget(endpoint, allowLoopback) || len(token) < 32 || len(token) > 512 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid fixed document source or credential")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}}
	return func(ctx context.Context, identity Identity, ids []string) (map[string]LocalDocument, error) {
		bad := errors.New("document source authorization, content or receipt invalid")
		if !validID(identity.ApplicationID) || !validID(identity.PrincipalID) || !list(ids, 64, validID) || len(ids) == 0 {
			return nil, bad
		}
		selected := append([]string{}, ids...)
		slices.Sort(selected)
		raw, _ := json.Marshal(struct {
			Identity
			WorkspaceSlug string   `json:"workspaceSlug"`
			Profile       string   `json:"profile"`
			DocumentIDs   []string `json:"documentIds"`
		}{identity, workspace, DocumentSourceProtocolVersion, selected})
		request, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
		if e != nil {
			return nil, bad
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Cache-Control", "no-store")
		response, e := client.Do(request)
		if e != nil {
			return nil, errors.New("document source unavailable")
		}
		defer response.Body.Close()
		kind, _, mimeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "" || mimeErr != nil || kind != "application/json" {
			return nil, bad
		}
		raw, e = io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		if e != nil || len(raw) > 8<<20 {
			return nil, bad
		}
		var out struct {
			Identity
			WorkspaceSlug string `json:"workspaceSlug"`
			Profile       string `json:"profile"`
			Documents     []struct {
				ID                     string    `json:"id"`
				Version                string    `json:"version"`
				ACLVersion             string    `json:"aclVersion"`
				Content                string    `json:"content"`
				ContentDigest          string    `json:"contentDigest"`
				Authorized             bool      `json:"authorized"`
				AuthorizationExpiresAt time.Time `json:"authorizationExpiresAt"`
			} `json:"documents"`
		}
		if strictJSON(bytes.NewReader(raw), &out) != nil || out.WorkspaceSlug != workspace || out.Profile != DocumentSourceProtocolVersion || out.Identity != identity || len(out.Documents) != len(selected) {
			return nil, bad
		}
		result := map[string]LocalDocument{}
		now, total := time.Now(), 0
		for _, doc := range out.Documents {
			sum := sha256.Sum256([]byte(doc.Content))
			total += len(doc.Content)
			if !slices.Contains(selected, doc.ID) || result[doc.ID].ID != "" || !validID(doc.Version) || !validID(doc.ACLVersion) || !doc.Authorized || !doc.AuthorizationExpiresAt.After(now) || doc.AuthorizationExpiresAt.After(now.Add(5*time.Minute)) || len(doc.Content) > 1<<20 || total > 4<<20 || !utf8.ValidString(doc.Content) || strings.ContainsRune(doc.Content, 0) || !digest.MatchString(doc.ContentDigest) || doc.ContentDigest != hex.EncodeToString(sum[:]) {
				return nil, bad
			}
			expires := doc.AuthorizationExpiresAt
			result[doc.ID] = LocalDocument{ID: doc.ID, Version: doc.Version, ACLVersion: doc.ACLVersion, Content: doc.Content, AuthorizationExpiresAt: &expires}
		}
		return result, nil
	}, nil
}
