package aisecurity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func testMediaPolicy() *MediaPolicy {
	return &MediaPolicy{AllowedMIMETypes: []string{"image/png", "image/jpeg", "audio/wav"}, MaxAssetBytes: 1 << 20, MaxTotalBytes: 2 << 20, MaxPixels: 2000000, InspectionVersion: MediaInspectionVersion}
}
func testPNG() []byte {
	im := image.NewRGBA(image.Rect(0, 0, 16, 16))
	im.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}
func mediaInput(data []byte, mime string) string {
	raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "Inspect this synthetic media"}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}}}}}})
	return string(raw)
}
func fakeMedia(text string, confidence float64) MediaProcessor {
	return func(_ context.Context, a MediaAsset) (MediaReport, error) {
		return MediaReport{Digest: a.Digest, MIMEType: a.MIMEType, Text: text, InspectionVersion: MediaInspectionVersion, Complete: true, Confidence: confidence}, nil
	}
}
func TestMediaRealBytesBindingProjectionAndPolicy(t *testing.T) {
	c := fixture()
	c.Models[0].Media = testMediaPolicy()
	for _, categories := range []*[]string{&c.Policy.AllowedInputCategories, &c.Policy.AllowedOutputCategories, &c.Models[0].AllowedInputCategories, &c.Models[0].AllowedOutputCategories} {
		*categories = append(*categories, "image")
	}
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = mediaInput(testPNG(), "image/png")
	if d := Evaluate(c, r, time.Now()); d.Code != "media_input_inspection_required" {
		t.Fatal("self-reported bytes became inspected", d)
	}
	prepared, e := PrepareMediaRequest(t.Context(), r, c.Models[0], fakeMedia("synthetic safe text", 99))
	if e != nil {
		t.Fatal(e)
	}
	if prepared.Input != r.Input || RequestDigest(prepared) != RequestDigest(r) || prepared.mediaInputBytes == 0 {
		t.Fatal("inspection changed source binding")
	}
	if d := Evaluate(c, prepared, time.Now()); d.Outcome != "allow" || len(d.MediaDigests) != 1 || d.MediaInspectionVersion != MediaInspectionVersion {
		t.Fatal(d)
	}
	prepared, e = PrepareMediaRequest(t.Context(), r, c.Models[0], fakeMedia("person@example.com", 99))
	if e != nil {
		t.Fatal(e)
	}
	if d := Evaluate(c, prepared, time.Now()); d.Outcome != "deny" {
		t.Fatal("OCR PII passed policy")
	}
	if _, e := PrepareMediaRequest(t.Context(), r, c.Models[0], nil); e == nil {
		t.Fatal("unavailable scanner accepted")
	}
	bad := func(ctx context.Context, a MediaAsset) (MediaReport, error) {
		v, _ := fakeMedia("safe", 99)(ctx, a)
		v.Digest = strings.Repeat("a", 64)
		return v, nil
	}
	if _, e := PrepareMediaRequest(t.Context(), r, c.Models[0], bad); e == nil {
		t.Fatal("unbound inspection receipt accepted")
	}
	prepared, e = PrepareMediaRequest(t.Context(), r, c.Models[0], fakeMedia("", 0))
	if e != nil {
		t.Fatal(e)
	}
	if d := Evaluate(c, prepared, time.Now()); d.Outcome != "deny" {
		t.Fatal("unknown visual content silently classified safe")
	}
}

func TestMediaProjectionIncludesForwardedGenerationText(t *testing.T) {
	c := fixture()
	c.Models[0].Media = testMediaPolicy()
	c.Policy.AllowedInputCategories = append(c.Policy.AllowedInputCategories, "image")
	c.Models[0].AllowedInputCategories = append(c.Models[0].AllowedInputCategories, "image")
	for _, input := range []string{`{"messages":[{"role":"user","content":"safe"}]}`, mediaInput(testPNG(), "image/png")} {
		var payload map[string]any
		_ = json.Unmarshal([]byte(input), &payload)
		payload["modalities"] = []string{"text", "audio"}
		payload["audio"] = map[string]string{"voice": "13800138000", "format": "wav"}
		raw, _ := json.Marshal(payload)
		r := testRequest("model")
		r.ModelID, r.Input = "enterprise", string(raw)
		prepared, err := PrepareMediaRequest(t.Context(), r, c.Models[0], fakeMedia("approved synthetic image", 99))
		if err != nil {
			t.Fatal(err)
		}
		if d := Evaluate(c, prepared, time.Now()); d.Outcome != "deny" || d.Code != "input_category_denied" {
			t.Fatal("forwarded voice bypassed data classification", d)
		}
	}
}

func TestMediaOutputRequiresContentAndTypedUsage(t *testing.T) {
	m := fixture().Models[0]
	m.Media = testMediaPolicy()
	for _, content := range []string{"null", "[]"} {
		raw := []byte(`{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":` + content + `},"finish_reason":"stop"}]}`)
		if _, err := normalizeModelOutputWithMedia(t.Context(), raw, m, nil); err == nil {
			t.Fatal("missing provider content accepted", content)
		}
	}
	raw := []byte(`{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	if _, err := normalizeModelOutputWithMedia(t.Context(), raw, m, nil); err != nil {
		t.Fatal("explicit empty text became incompatible", err)
	}
	for _, usage := range []string{`{"total_tokens":-1}`, `{"total_tokens":"many"}`, `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":3}`} {
		invalid := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"usage":`+usage+`}`)...)
		if _, err := normalizeModelOutputWithMedia(t.Context(), invalid, m, nil); err == nil {
			t.Fatal("invalid media usage accepted")
		}
	}
}
func TestMediaCanonicalizationAndMalformedAssets(t *testing.T) {
	p := testMediaPolicy()
	im := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var jpegBytes bytes.Buffer
	_ = jpeg.Encode(&jpegBytes, im, nil)
	withTrailer := append(jpegBytes.Bytes(), []byte("hidden metadata person@example.com")...)
	a, e := canonicalMedia("image/jpeg", withTrailer, p)
	if e != nil {
		t.Fatal(e)
	}
	b, e := canonicalMedia(a.MIMEType, a.Data, p)
	if e != nil || a.Digest != b.Digest || bytes.Contains(a.Data, []byte("person@example.com")) {
		t.Fatal("media sanitization not stable")
	}
	for _, raw := range []string{"https://example.invalid/image.png", "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>")), "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not an image"))} {
		if _, e := mediaDataURI(raw, p); e == nil {
			t.Fatal("unsafe asset accepted")
		}
	}
	p.MaxPixels = 1
	if _, e := canonicalMedia("image/png", testPNG(), p); e == nil {
		t.Fatal("pixel bomb budget ignored")
	}
	p = testMediaPolicy()
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(36+3200))
	wav.WriteString("WAVEfmt ")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(16))
	for _, v := range []any{uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		_ = binary.Write(&wav, binary.LittleEndian, v)
	}
	wav.WriteString("data")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(3200))
	wav.Write(make([]byte, 3200))
	if _, e := canonicalMedia("audio/wav", wav.Bytes(), p); e != nil {
		t.Fatal(e)
	}
	bad := append([]byte{}, wav.Bytes()...)
	binary.LittleEndian.PutUint32(bad[24:], 44100)
	if _, e := canonicalMedia("audio/wav", bad, p); e == nil {
		t.Fatal("unsupported PCM accepted")
	}
}
func TestMediaOutputInspectActualImageAndSuppressPII(t *testing.T) {
	m := fixture().Models[0]
	m.Media = testMediaPolicy()
	m.ResponseFormat = "openai_chat"
	raw, _ := json.Marshal(map[string]any{"object": "chat.completion", "model": "enterprise", "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG())}}}}}}})
	normalized, e := normalizeModelOutputWithMedia(t.Context(), raw, m, fakeMedia("person@example.com", 99))
	if e != nil {
		t.Fatal(e)
	}
	r := testRequest("model")
	r.ModelID = m.ID
	r.Input = `{"prompt":"safe"}`
	r.Output = string(normalized)
	r, e = ApplyModelOutputInspection(t.Context(), r, normalized, m, fakeMedia("person@example.com", 99))
	if e != nil {
		t.Fatal(e)
	}
	c := fixture()
	c.Models[0] = m
	if d := Evaluate(c, r, time.Now()); d.Outcome != "deny" {
		t.Fatal("media output skipped OCR DLP")
	}
	if _, e := normalizeModelOutputWithMedia(t.Context(), raw, m, nil); e == nil {
		t.Fatal("media response released without scanner")
	}
}
func TestCommandMediaProcessorActualOCR(t *testing.T) {
	path, e := exec.LookPath("tesseract")
	if e != nil {
		t.Skip("tesseract not installed")
	}
	processor, e := NewCommandMediaProcessor(CommandMediaOptions{Tesseract: path, Languages: "eng"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("testdata/media-email.png")
	if e != nil {
		t.Fatal(e)
	}
	a, e := canonicalMedia("image/png", raw, testMediaPolicy())
	if e != nil {
		t.Fatal(e)
	}
	report, e := processor(t.Context(), a)
	if e != nil || !strings.Contains(report.Text, "person@example.com") || !report.Complete {
		t.Fatalf("OCR synthetic fixture failed: complete=%v err=%v", report.Complete, e)
	}
	found, code := inspect(report.Text, 65536, nil)
	if code != "" || len(found) == 0 {
		t.Fatal("recognized content not inspected")
	}
}
