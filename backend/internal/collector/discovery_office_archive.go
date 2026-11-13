package collector

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"strings"
)

// Office parts are read in memory, never extracted to a filesystem. Reject
// ambiguous names as well as bounding declared expansion before opening a part.
func openOfficeArchive(raw []byte) (*zip.Reader, map[string]*zip.File, string) {
	if len(raw) > MaxFileBytes {
		return nil, nil, "archive_limit"
	}
	archive, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		return nil, nil, "invalid_document"
	}
	if len(archive.File) > 2000 {
		return nil, nil, "archive_limit"
	}
	parts := map[string]*zip.File{}
	seen := map[string]bool{}
	var total uint64
	for _, part := range archive.File {
		name := strings.TrimSuffix(part.Name, "/")
		if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00") || part.Mode()&fs.ModeSymlink != 0 {
			return nil, nil, "archive_path_invalid"
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, nil, "archive_duplicate_entry"
		}
		seen[key] = true
		if part.UncompressedSize64 > 8<<20 || total > (32<<20)-part.UncompressedSize64 {
			return nil, nil, "archive_limit"
		}
		total += part.UncompressedSize64
		if !part.FileInfo().IsDir() {
			parts[part.Name] = part
		}
	}
	return archive, parts, ""
}
