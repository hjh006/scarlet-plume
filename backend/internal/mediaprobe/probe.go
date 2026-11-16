// Package mediaprobe verifies the actual private scanner protocol with fixed
// synthetic media. It never invokes a model provider or a governance admission.
package mediaprobe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func LoadCA(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("scanner CA requires an absolute path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("scanner CA unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, errors.New("scanner CA invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("scanner CA invalid")
	}
	return raw, nil
}

func Formats(c aisecurity.Config) []string {
	var formats []string
	for _, m := range c.Models {
		if !m.Enabled || m.Media == nil {
			continue
		}
		for _, mime := range m.Media.AllowedMIMETypes {
			if mime == "image/jpeg" {
				mime = "image/png"
			}
			if !slices.Contains(formats, mime) {
				formats = append(formats, mime)
			}
		}
	}
	return formats
}

// Check uses the same canonicalization and version-bound processor as execution.
// Blank pixels and silent PCM contain no customer content; recognition quality
// or a successful customer inference is deliberately not inferred from readiness.
func Check(ctx context.Context, processor aisecurity.MediaProcessor, formats []string) error {
	if len(formats) == 0 {
		return nil
	}
	if len(formats) > 2 {
		return errors.New("readiness format budget exceeded")
	}
	if processor == nil {
		return errors.New("media processor unavailable")
	}
	policy := &aisecurity.MediaPolicy{AllowedMIMETypes: []string{"image/png", "audio/wav"}, MaxAssetBytes: 65536, MaxTotalBytes: 131072, MaxPixels: 4096, InspectionVersion: aisecurity.MediaInspectionVersion}
	for _, format := range formats {
		if ctx.Err() != nil {
			return errors.New("media readiness cancelled")
		}
		var part any
		switch format {
		case "image/png":
			im := image.NewRGBA(image.Rect(0, 0, 64, 64))
			for y := 0; y < 64; y++ {
				for x := 0; x < 64; x++ {
					im.Set(x, y, color.White)
				}
			}
			var raw bytes.Buffer
			if png.Encode(&raw, im) != nil {
				return errors.New("media readiness fixture unavailable")
			}
			part = map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw.Bytes())}}
		case "audio/wav":
			raw := make([]byte, 44+32000)
			copy(raw, "RIFF")
			binary.LittleEndian.PutUint32(raw[4:], uint32(len(raw)-8))
			copy(raw[8:], "WAVEfmt ")
			binary.LittleEndian.PutUint32(raw[16:], 16)
			binary.LittleEndian.PutUint16(raw[20:], 1)
			binary.LittleEndian.PutUint16(raw[22:], 1)
			binary.LittleEndian.PutUint32(raw[24:], 16000)
			binary.LittleEndian.PutUint32(raw[28:], 32000)
			binary.LittleEndian.PutUint16(raw[32:], 2)
			binary.LittleEndian.PutUint16(raw[34:], 16)
			copy(raw[36:], "data")
			binary.LittleEndian.PutUint32(raw[40:], 32000)
			part = map[string]any{"type": "input_audio", "input_audio": map[string]string{"format": "wav", "data": base64.StdEncoding.EncodeToString(raw)}}
		default:
			return errors.New("unsupported readiness format")
		}
		input, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{part}}}})
		if _, err := aisecurity.PrepareMediaRequest(ctx, aisecurity.Request{Input: string(input)}, aisecurity.Model{ID: "media-readiness", Media: policy}, processor); err != nil {
			return errors.New("media readiness or version unavailable")
		}
	}
	return nil
}
