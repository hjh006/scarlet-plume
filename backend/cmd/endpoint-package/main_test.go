package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var publicKitAssets = []string{"README.md", "config.example.json", "Install-ScarletEndpoint.ps1", "Remove-ScarletEndpoint.ps1", "Test-ScarletDeployment.ps1", "functional-checklist.csv", "clipboard-config.example.json", "ScarletClipboard.Common.ps1", "Install-ScarletClipboardSession.ps1", "Start-ScarletClipboardSession.ps1", "Remove-ScarletClipboardSession.ps1", "Test-ScarletClipboardSession.ps1", "Invoke-ScarletClipboardText.ps1", "clipboard-functional-checklist.csv"}

type packageFixture struct {
	source, binaries, output string
	want                     map[string][]byte
}

func packageInputs(t *testing.T) packageFixture {
	t.Helper()
	root := t.TempDir()
	f := packageFixture{source: filepath.Join(root, "public"), binaries: filepath.Join(root, "compiled"), output: filepath.Join(root, "out", "deployment.zip"), want: map[string][]byte{}}
	for _, directory := range []string{f.source, f.binaries, filepath.Dir(f.output)} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range publicKitAssets {
		raw := []byte("synthetic public asset: " + name + "\n")
		if err := os.WriteFile(filepath.Join(f.source, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		f.want[name] = raw
	}
	for name, machine := range map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64} {
		name = "scarlet-endpoint-agent-windows-" + name + ".exe"
		raw := tinyPE(t, machine)
		if err := os.WriteFile(filepath.Join(f.binaries, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		f.want[name] = raw
	}
	return f
}

// A structurally parseable PE32+ header is enough for architecture checks.
// It is intentionally not a runnable Windows agent or a runtime-acceptance fixture.
func tinyPE(t *testing.T, machine uint16) []byte {
	t.Helper()
	var raw bytes.Buffer
	dos := make([]byte, 128)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], uint32(len(dos)))
	raw.Write(dos)
	raw.Write([]byte{'P', 'E', 0, 0})
	optional := pe.OptionalHeader64{Magic: 0x20b, SizeOfHeaders: 512, NumberOfRvaAndSizes: 16}
	if err := binary.Write(&raw, binary.LittleEndian, pe.FileHeader{Machine: machine, SizeOfOptionalHeader: uint16(binary.Size(optional)), Characteristics: pe.IMAGE_FILE_EXECUTABLE_IMAGE}); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&raw, binary.LittleEndian, optional); err != nil {
		t.Fatal(err)
	}
	file, err := pe.NewFile(bytes.NewReader(raw.Bytes()))
	if err != nil || file.Machine != machine {
		t.Fatal("invalid architecture fixture", err)
	}
	_ = file.Close()
	return raw.Bytes()
}

func zipContents(t *testing.T, path string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	result := map[string][]byte{}
	for _, file := range r.File {
		if _, duplicate := result[file.Name]; duplicate || file.Name != filepath.Base(file.Name) || strings.ContainsAny(file.Name, "/\\") || !file.Mode().IsRegular() || file.Mode().Perm() != 0644 {
			t.Fatal("unsafe or duplicate ZIP entry", file.Name)
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name] = raw
	}
	return result
}

func TestPackageWhitelistManifestAndStandaloneMatch(t *testing.T) {
	f := packageInputs(t)
	secret := []byte("synthetic-private-token-MUST-NOT-BE-PUBLISHED")
	for _, path := range []string{filepath.Join(f.source, ".env"), filepath.Join(f.source, "runtime.json"), filepath.Join(f.binaries, "unlisted.exe"), filepath.Join(f.binaries, "credentials.pem")} {
		if err := os.WriteFile(path, secret, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := build(f.source, f.binaries, f.output); err != nil {
		t.Fatal(err)
	}
	contents := zipContents(t, f.output)
	if len(contents) != len(f.want)+1 {
		t.Fatal("ZIP did not use the exact public whitelist")
	}
	for name, raw := range contents {
		if bytes.Contains(raw, secret) {
			t.Fatal("unlisted secret content entered the package", name)
		}
		if name != "manifest.json" && !bytes.Equal(raw, f.want[name]) {
			t.Fatal("unexpected, missing or modified public asset", name)
		}
	}
	standalone, err := os.ReadFile(filepath.Join(filepath.Dir(f.output), "windows-enterprise-manifest.json"))
	if err != nil || !bytes.Equal(standalone, contents["manifest.json"]) {
		t.Fatal("standalone and ZIP manifests differ", err)
	}
	var manifest struct {
		Version                           string `json:"version"`
		NativeRuntimeAcceptance           string `json:"nativeRuntimeAcceptance"`
		ClipboardSessionRuntimeAcceptance string `json:"clipboardSessionRuntimeAcceptance"`
		Files                             []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(standalone, &manifest); err != nil || manifest.Version != "windows-enterprise-v2" || manifest.NativeRuntimeAcceptance != "pending" || manifest.ClipboardSessionRuntimeAcceptance != "pending" || len(manifest.Files) != len(f.want) {
		t.Fatal("invalid manifest or unverified Windows acceptance was promoted", err)
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Files {
		raw, exists := f.want[entry.Path]
		sum := sha256.Sum256(raw)
		if !exists || seen[entry.Path] || entry.Bytes != len(raw) || entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("manifest does not describe the exact archived bytes", entry.Path)
		}
		seen[entry.Path] = true
	}
	first, _ := os.ReadFile(f.output)
	if err := build(f.source, f.binaries, f.output); err != nil {
		t.Fatal("cannot atomically replace an existing regular output", err)
	}
	second, _ := os.ReadFile(f.output)
	if !bytes.Equal(first, second) {
		t.Fatal("same public inputs produced different ZIP bytes")
	}
}

func TestPackageRejectsWrongArchitectureMalformedAndOversizeInputs(t *testing.T) {
	for _, name := range []string{"amd64_contains_arm64", "arm64_contains_amd64", "not_pe", "truncated_pe", "missing_binary", "oversize_binary", "missing_public_asset", "oversize_public_asset", "public_asset_directory"} {
		t.Run(name, func(t *testing.T) {
			f := packageInputs(t)
			path := filepath.Join(f.binaries, "scarlet-endpoint-agent-windows-amd64.exe")
			var err error
			switch name {
			case "amd64_contains_arm64":
				err = os.WriteFile(path, tinyPE(t, pe.IMAGE_FILE_MACHINE_ARM64), 0600)
			case "arm64_contains_amd64":
				err = os.WriteFile(filepath.Join(f.binaries, "scarlet-endpoint-agent-windows-arm64.exe"), tinyPE(t, pe.IMAGE_FILE_MACHINE_AMD64), 0600)
			case "not_pe":
				err = os.WriteFile(path, []byte(strings.Repeat("not-a-Windows-binary", 20)), 0600)
			case "truncated_pe":
				err = os.WriteFile(path, tinyPE(t, pe.IMAGE_FILE_MACHINE_AMD64)[:80], 0600)
			case "missing_binary":
				err = os.Remove(path)
			case "oversize_binary":
				err = os.Truncate(path, (64<<20)+1) // Sparse file: no per-test cross-build or large allocation.
			case "missing_public_asset":
				err = os.Remove(filepath.Join(f.source, "README.md"))
			case "oversize_public_asset":
				err = os.Truncate(filepath.Join(f.source, "README.md"), (128<<10)+1)
			case "public_asset_directory":
				path = filepath.Join(f.source, "README.md")
				err = os.Remove(path)
				if err == nil {
					err = os.Mkdir(path, 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := build(f.source, f.binaries, f.output); err == nil {
				t.Fatal("invalid input was packaged")
			}
			for _, path := range []string{f.output, filepath.Join(filepath.Dir(f.output), "windows-enterprise-manifest.json")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("input validation failure published an output", path, err)
				}
			}
		})
	}
}

func TestPackageRejectsSymlinkRootsAssetsAndOutputs(t *testing.T) {
	for _, name := range []string{"source_root", "source_root_trailing_separator", "binaries_root", "output_directory", "public_asset", "binary_asset", "zip_output", "manifest_output"} {
		t.Run(name, func(t *testing.T) {
			f := packageInputs(t)
			protected := filepath.Join(t.TempDir(), "private-fixture")
			private := []byte("synthetic-protected-file-remains-unchanged")
			if err := os.WriteFile(protected, private, 0600); err != nil {
				t.Fatal(err)
			}
			var link, target string
			switch name {
			case "source_root", "source_root_trailing_separator":
				link, target = f.source+"-alias", f.source
				f.source = link
			case "binaries_root":
				link, target = f.binaries+"-alias", f.binaries
				f.binaries = link
			case "output_directory":
				link, target = filepath.Dir(f.output)+"-alias", filepath.Dir(f.output)
				f.output = filepath.Join(link, "deployment.zip")
			case "public_asset":
				link, target = filepath.Join(f.source, "README.md"), protected
			case "binary_asset":
				link, target = filepath.Join(f.binaries, "scarlet-endpoint-agent-windows-amd64.exe"), protected
			case "zip_output":
				link, target = f.output, protected
			case "manifest_output":
				link, target = filepath.Join(filepath.Dir(f.output), "windows-enterprise-manifest.json"), protected
			}
			if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if name == "source_root_trailing_separator" {
				f.source += string(os.PathSeparator)
			}
			if err := build(f.source, f.binaries, f.output); err == nil {
				t.Fatal("symlink input/output was accepted")
			}
			got, err := os.ReadFile(protected)
			if err != nil || !bytes.Equal(got, private) {
				t.Fatal("package builder overwrote a symlink target", err)
			}
		})
	}
}

func TestAtomicOutputFailurePreservesPriorArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployment.zip")
	prior := []byte("previous complete artifact")
	if err := os.WriteFile(path, prior, 0644); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("synthetic interrupted write")
	err := writeAtomic(path, func(file *os.File) error { _, _ = file.Write([]byte("incomplete new artifact")); return interrupted })
	if !errors.Is(err, interrupted) {
		t.Fatal("write failure was hidden", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, prior) {
		t.Fatal("failed package write truncated prior artifact", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatal("failed write leaked temporary output", err)
	}
}
