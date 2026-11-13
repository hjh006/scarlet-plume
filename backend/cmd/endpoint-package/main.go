// endpoint-package builds a bounded deployment kit from explicit public assets
// and the two freshly built Windows binaries. It never discovers local secrets.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type entry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

func main() {
	source := flag.String("source", "", "explicit public kit directory")
	binaries := flag.String("binaries", "", "explicit compiled binary directory")
	out := flag.String("out", "", "deployment zip output")
	flag.Parse()
	if flag.NArg() != 0 || build(*source, *binaries, *out) != nil {
		fmt.Fprintln(os.Stderr, "deployment package build failed; check explicit public sources and Windows binaries")
		os.Exit(1)
	}
}
func build(source, binaries, out string) error {
	if source == "" || binaries == "" || out == "" {
		return errors.New("explicit package directories and output are required")
	}
	source, binaries, out = filepath.Clean(source), filepath.Clean(binaries), filepath.Clean(out)
	for _, directory := range []string{source, binaries, filepath.Dir(out)} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("package directory must be a real directory")
		}
	}
	manifestPath := filepath.Join(filepath.Dir(out), "windows-enterprise-manifest.json")
	if out == manifestPath {
		return errors.New("package and manifest outputs must be distinct")
	}
	for _, path := range []string{out, manifestPath} {
		if err := validOutput(path); err != nil {
			return err
		}
	}
	assets := []string{"README.md", "config.example.json", "Install-ScarletEndpoint.ps1", "Remove-ScarletEndpoint.ps1", "Test-ScarletDeployment.ps1", "functional-checklist.csv", "clipboard-config.example.json", "ScarletClipboard.Common.ps1", "Install-ScarletClipboardSession.ps1", "Start-ScarletClipboardSession.ps1", "Remove-ScarletClipboardSession.ps1", "Test-ScarletClipboardSession.ps1", "Invoke-ScarletClipboardText.ps1", "clipboard-functional-checklist.csv"}
	content := map[string][]byte{}
	manifest := struct {
		Version                           string  `json:"version"`
		NativeRuntimeAcceptance           string  `json:"nativeRuntimeAcceptance"`
		ClipboardSessionRuntimeAcceptance string  `json:"clipboardSessionRuntimeAcceptance"`
		Files                             []entry `json:"files"`
	}{Version: "windows-enterprise-v2", NativeRuntimeAcceptance: "pending", ClipboardSessionRuntimeAcceptance: "pending"}
	add := func(base, name string, limit int64) error {
		path := filepath.Join(base, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			return errors.New("invalid package asset")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		content[name] = raw
		manifest.Files = append(manifest.Files, entry{Path: name, SHA256: hex.EncodeToString(sum[:]), Bytes: len(raw)})
		return nil
	}
	for _, name := range assets {
		if err := add(source, name, 128<<10); err != nil {
			return err
		}
	}
	for _, arch := range []struct {
		name    string
		machine uint16
	}{{"amd64", pe.IMAGE_FILE_MACHINE_AMD64}, {"arm64", pe.IMAGE_FILE_MACHINE_ARM64}} {
		name := "scarlet-endpoint-agent-windows-" + arch.name + ".exe"
		if err := add(binaries, name, 64<<20); err != nil {
			return err
		}
		binary, err := pe.NewFile(bytes.NewReader(content[name]))
		if err != nil {
			return err
		}
		machine := binary.Machine
		binary.Close()
		if machine != arch.machine {
			return errors.New("Windows architecture mismatch")
		}
		assets = append(assets, name)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// Served separately over the configured platform HTTPS origin for the
	// installer's independent expected-binary-hash input.
	content["manifest.json"] = raw
	assets = append(assets, "manifest.json")
	if err = writeAtomic(out, func(file *os.File) error {
		writer := zip.NewWriter(file)
		for _, name := range assets {
			h := &zip.FileHeader{Name: name, Method: zip.Deflate}
			h.SetMode(0644)
			entry, err := writer.CreateHeader(h)
			if err != nil {
				_ = writer.Close()
				return err
			}
			if _, err = entry.Write(content[name]); err != nil {
				_ = writer.Close()
				return err
			}
		}
		return writer.Close()
	}); err != nil {
		return err
	}
	return writeAtomic(manifestPath, func(file *os.File) error { _, err := file.Write(raw); return err })
}

func validOutput(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("package output must be a regular file or absent")
	}
	return nil
}

// Each completed public output replaces its path atomically, never a symlink target.
func writeAtomic(path string, write func(*os.File) error) error {
	if err := validOutput(path); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if err = file.Chmod(0644); err != nil {
		return err
	}
	if err = write(file); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = validOutput(path); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
