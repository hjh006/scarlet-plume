package main

import (
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateConfigurationRejectsUnknownFieldsPermissionsAndTrailingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	for _, tc := range []struct {
		name, raw string
		mode      os.FileMode
		ok        bool
	}{{"private valid", `{"credentials":[],"targets":{}}`, 0600, true}, {"unknown upstream override", `{"credentials":[],"targets":{},"upstream":"http://elsewhere"}`, 0600, false}, {"trailing payload", `{"credentials":[],"targets":{}} {}`, 0600, false}, {"world readable", `{"credentials":[],"targets":{}}`, 0644, false}, {"oversized", `{"credentials":[],"targets":{}}`, 0600, false}} {
		t.Run(tc.name, func(t *testing.T) {
			if e := os.WriteFile(path, []byte(tc.raw), tc.mode); e != nil {
				t.Fatal(e)
			}
			if e := os.Chmod(path, tc.mode); e != nil {
				t.Fatal(e)
			}
			var value bindingFile
			limit := int64(4096)
			if tc.name == "oversized" {
				limit = 4
			}
			e := decodePrivate(path, limit, &value)
			if (e == nil) != tc.ok {
				t.Fatal(e)
			}
		})
	}
}
func TestDocumentSourceDoesNotAcceptACLOrIdentityAssertions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.json")
	_ = os.WriteFile(path, []byte(`[{"id":"doc","version":"v1","content":"text","allowedPrincipalIds":["anyone"]}]`), 0600)
	var rows []aisecurity.LocalDocument
	if decodePrivate(path, 4096, &rows) == nil {
		t.Fatal("document content source was allowed to override trusted catalog ACL")
	}
}
