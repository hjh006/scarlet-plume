package aisecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"math"
	"slices"
	"strings"
)

const MediaInspectionVersion = "scarlet-media-ocr-asr-v1"

type MediaPolicy struct {
	AllowedMIMETypes  []string `json:"allowedMimeTypes"`
	MaxAssetBytes     int      `json:"maxAssetBytes"`
	MaxTotalBytes     int      `json:"maxTotalBytes"`
	MaxPixels         int      `json:"maxPixels"`
	InspectionVersion string   `json:"inspectionVersion"`
}
type MediaAsset struct {
	MIMEType string `json:"mimeType"`
	Data     []byte `json:"data"`
	Digest   string `json:"digest"`
}
type MediaReport struct {
	Digest            string  `json:"digest"`
	MIMEType          string  `json:"mimeType"`
	Text              string  `json:"text"`
	InspectionVersion string  `json:"inspectionVersion"`
	Complete          bool    `json:"complete"`
	Confidence        float64 `json:"confidence"`
}
type MediaProcessor func(context.Context, MediaAsset) (MediaReport, error)
type MediaOutput struct {
	Type     string `json:"type"`
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
	Digest   string `json:"digest"`
}
type modelContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	} `json:"image_url,omitempty"`
	InputAudio *struct {
		Data   string `json:"data"`
		Format string `json:"format"`
	} `json:"input_audio,omitempty"`
}
type modelMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}
type modelPayload struct {
	Model         string         `json:"model,omitempty"`
	Prompt        *string        `json:"prompt,omitempty"`
	Input         *string        `json:"input,omitempty"`
	Messages      []modelMessage `json:"messages,omitempty"`
	Stream        bool           `json:"stream"`
	MaxTokens     int            `json:"max_tokens"`
	Temperature   *float64       `json:"temperature,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Modalities []string `json:"modalities,omitempty"`
	Audio      *struct {
		Voice  string `json:"voice"`
		Format string `json:"format"`
	} `json:"audio,omitempty"`
}

func validateMediaPolicy(p *MediaPolicy) bool {
	if p == nil {
		return true
	}
	return p.InspectionVersion == MediaInspectionVersion && list(p.AllowedMIMETypes, 3, func(s string) bool { return slices.Contains([]string{"image/png", "image/jpeg", "audio/wav"}, s) }) && len(p.AllowedMIMETypes) > 0 && p.MaxAssetBytes > 0 && p.MaxAssetBytes <= 4<<20 && p.MaxTotalBytes >= p.MaxAssetBytes && p.MaxTotalBytes <= 8<<20 && p.MaxPixels > 0 && p.MaxPixels <= 16000000
}
func mediaHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// Canonicalize pixels/PCM before inspection and forwarding. Metadata and trailing
// payloads cannot be a separate uninspected data channel to the provider.
func canonicalMedia(mime string, data []byte, p *MediaPolicy) (MediaAsset, error) {
	bad := errors.New("media type, structure or budget unsupported")
	if p == nil || !validateMediaPolicy(p) || !slices.Contains(p.AllowedMIMETypes, mime) || len(data) == 0 || len(data) > p.MaxAssetBytes {
		return MediaAsset{}, bad
	}
	var out bytes.Buffer
	switch mime {
	case "image/png", "image/jpeg":
		cfg, format, e := image.DecodeConfig(bytes.NewReader(data))
		if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > p.MaxPixels/cfg.Height || mime != "image/"+format {
			return MediaAsset{}, bad
		}
		img, _, e := image.Decode(bytes.NewReader(data))
		if e != nil {
			return MediaAsset{}, bad
		}
		e = png.Encode(&out, img)
		mime = "image/png"
		if e != nil {
			return MediaAsset{}, bad
		}
	case "audio/wav":
		if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" || int64(binary.LittleEndian.Uint32(data[4:8]))+8 != int64(len(data)) {
			return MediaAsset{}, bad
		}
		var format, pcm []byte
		for pos := 12; pos < len(data); {
			if pos+8 > len(data) {
				return MediaAsset{}, bad
			}
			size := int64(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
			end := int64(pos) + 8 + size
			if end > int64(len(data)) {
				return MediaAsset{}, bad
			}
			switch string(data[pos : pos+4]) {
			case "fmt ":
				if format != nil || size != 16 {
					return MediaAsset{}, bad
				}
				format = data[pos+8 : int(end)]
			case "data":
				if pcm != nil {
					return MediaAsset{}, bad
				}
				pcm = data[pos+8 : int(end)]
			}
			pos = int(end) + (int(size) & 1)
			if pos > len(data) {
				return MediaAsset{}, bad
			}
		}
		if len(format) != 16 || binary.LittleEndian.Uint16(format) != 1 || binary.LittleEndian.Uint16(format[2:]) != 1 || binary.LittleEndian.Uint32(format[4:]) != 16000 || binary.LittleEndian.Uint32(format[8:]) != 32000 || binary.LittleEndian.Uint16(format[12:]) != 2 || binary.LittleEndian.Uint16(format[14:]) != 16 || len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > 30*32000 {
			return MediaAsset{}, bad
		}
		out.WriteString("RIFF")
		_ = binary.Write(&out, binary.LittleEndian, uint32(36+len(pcm)))
		out.WriteString("WAVEfmt ")
		_ = binary.Write(&out, binary.LittleEndian, uint32(16))
		out.Write(format)
		out.WriteString("data")
		_ = binary.Write(&out, binary.LittleEndian, uint32(len(pcm)))
		out.Write(pcm)
	default:
		return MediaAsset{}, bad
	}
	if out.Len() > p.MaxAssetBytes {
		return MediaAsset{}, bad
	}
	raw := out.Bytes()
	return MediaAsset{MIMEType: mime, Data: raw, Digest: mediaHash(raw)}, nil
}
func mediaDataURI(raw string, p *MediaPolicy) (MediaAsset, error) {
	if !strings.HasPrefix(raw, "data:") {
		return MediaAsset{}, errors.New("remote media URLs are not allowed")
	}
	header, encoded, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	mime, base64ok := strings.CutSuffix(header, ";base64")
	if !ok || !base64ok || p == nil || len(encoded) > base64.StdEncoding.EncodedLen(p.MaxAssetBytes) {
		return MediaAsset{}, errors.New("media encoding invalid")
	}
	data, e := base64.StdEncoding.Strict().DecodeString(encoded)
	if e != nil {
		return MediaAsset{}, e
	}
	return canonicalMedia(mime, data, p)
}
func mediaPart(part *modelContentPart, p *MediaPolicy) (MediaAsset, error) {
	switch part.Type {
	case "image_url":
		if part.ImageURL == nil || part.InputAudio != nil || part.Text != "" || part.ImageURL.Detail != "" && part.ImageURL.Detail != "auto" && part.ImageURL.Detail != "low" && part.ImageURL.Detail != "high" {
			break
		}
		a, e := mediaDataURI(part.ImageURL.URL, p)
		if e != nil || !strings.HasPrefix(a.MIMEType, "image/") {
			return MediaAsset{}, errors.New("invalid image")
		}
		part.ImageURL.URL = "data:" + a.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
		return a, nil
	case "input_audio":
		if part.InputAudio == nil || part.ImageURL != nil || part.Text != "" || part.InputAudio.Format != "wav" || p == nil || len(part.InputAudio.Data) > base64.StdEncoding.EncodedLen(p.MaxAssetBytes) {
			break
		}
		data, e := base64.StdEncoding.Strict().DecodeString(part.InputAudio.Data)
		if e != nil {
			return MediaAsset{}, e
		}
		a, e := canonicalMedia("audio/wav", data, p)
		if e == nil {
			part.InputAudio.Data = base64.StdEncoding.EncodeToString(a.Data)
		}
		return a, e
	}
	return MediaAsset{}, errors.New("unsupported content part")
}
func parseModelPayload(input string, model Model) (modelPayload, []MediaAsset, []string, error) {
	var p modelPayload
	bad := errors.New("unsupported model payload")
	if strictJSON(strings.NewReader(input), &p) != nil || len(p.Messages) > 128 || p.MaxTokens < 0 || p.MaxTokens > 4096 || p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) || !validateMediaPolicy(model.Media) {
		return p, nil, nil, bad
	}
	expected := model.UpstreamModel
	if expected == "" {
		expected = model.ID
	}
	if p.Model != "" && p.Model != expected {
		return p, nil, nil, bad
	}
	p.Model = expected
	shapes := 0
	texts := []string{}
	assets := []MediaAsset{}
	total := 0
	if p.Prompt != nil {
		shapes++
		texts = append(texts, *p.Prompt)
	}
	if p.Input != nil {
		shapes++
		texts = append(texts, *p.Input)
	}
	if len(p.Messages) > 0 {
		shapes++
		for i := range p.Messages {
			msg := &p.Messages[i]
			if !slices.Contains([]string{"system", "user", "assistant"}, msg.Role) {
				return p, nil, nil, bad
			}
			var text string
			if json.Unmarshal(msg.Content, &text) == nil && string(msg.Content) != "null" {
				texts = append(texts, text)
				continue
			}
			var parts []modelContentPart
			if strictJSON(bytes.NewReader(msg.Content), &parts) != nil || len(parts) == 0 || len(parts) > 16 || model.Media == nil {
				return p, nil, nil, bad
			}
			for j := range parts {
				part := &parts[j]
				if part.Type == "text" {
					if part.ImageURL != nil || part.InputAudio != nil {
						return p, nil, nil, bad
					}
					texts = append(texts, part.Text)
					continue
				}
				a, e := mediaPart(part, model.Media)
				if e != nil {
					return p, nil, nil, e
				}
				total += len(a.Data)
				if total > model.Media.MaxTotalBytes || len(assets) >= 16 {
					return p, nil, nil, bad
				}
				assets = append(assets, a)
			}
			msg.Content, _ = json.Marshal(parts)
		}
	}
	if shapes != 1 {
		return p, nil, nil, bad
	}
	if p.MaxTokens == 0 {
		p.MaxTokens = 1024
	}
	if p.Stream {
		p.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{true}
	} else if p.StreamOptions != nil {
		return p, nil, nil, bad
	}
	if len(p.Modalities) > 0 {
		if model.Media == nil || !list(p.Modalities, 2, func(v string) bool { return v == "text" || v == "audio" }) || !slices.Contains(p.Modalities, "text") {
			return p, nil, nil, bad
		}
		if slices.Contains(p.Modalities, "audio") && (p.Stream || p.Audio == nil || p.Audio.Format != "wav" || !validID(p.Audio.Voice) || !slices.Contains(model.Media.AllowedMIMETypes, "audio/wav")) {
			return p, nil, nil, bad
		}
	}
	if p.Audio != nil && !slices.Contains(p.Modalities, "audio") {
		return p, nil, nil, bad
	}
	if p.Audio != nil {
		// Recognition projections replace the raw JSON during inspection. Keep
		// every forwarded caller-controlled string, including generation options.
		texts = append(texts, p.Audio.Voice)
	}
	return p, assets, texts, nil
}
func modelRequestBody(input string, model Model) ([]byte, error) {
	p, _, _, e := parseModelPayload(input, model)
	if e != nil {
		return nil, e
	}
	return json.Marshal(p)
}
func modelRequestStream(input string) (bool, error) {
	var p struct {
		Stream bool `json:"stream"`
	}
	e := json.Unmarshal([]byte(input), &p)
	return p.Stream, e
}
func ModelRequestStream(input string) (bool, error) { return modelRequestStream(input) }

func inspectMediaAssets(ctx context.Context, assets []MediaAsset, processor MediaProcessor) (string, []string, error) {
	texts := []string{}
	categories := []string{}
	if len(assets) > 0 && processor == nil {
		return "", nil, errors.New("media inspection unavailable")
	}
	for _, asset := range assets {
		r, e := processor(ctx, asset)
		if e != nil || !r.Complete || r.Digest != asset.Digest || r.MIMEType != asset.MIMEType || r.InspectionVersion != MediaInspectionVersion || !bounded(r.Text, 1<<20) || math.IsNaN(r.Confidence) || math.IsInf(r.Confidence, 0) || r.Confidence < 0 || r.Confidence > 100 {
			return "", nil, errors.New("media inspection incomplete or unbound")
		}
		texts = append(texts, r.Text)
		if strings.HasPrefix(asset.MIMEType, "image/") {
			categories = append(categories, "image")
		} else {
			categories = append(categories, "audio")
		}
		if strings.TrimSpace(r.Text) == "" || r.Confidence < 90 {
			categories = append(categories, "unclassified_media")
		}
	}
	return strings.Join(texts, "\n"), unique(categories), nil
}
func PrepareMediaRequest(ctx context.Context, r Request, model Model, processor MediaProcessor) (Request, error) {
	_, assets, texts, e := parseModelPayload(r.Input, model)
	if e != nil {
		return r, e
	}
	if len(assets) == 0 {
		return r, nil
	}
	projection, cats, e := inspectMediaAssets(ctx, assets, processor)
	if e != nil {
		return r, e
	}
	text := strings.Join(append(texts, projection), "\n")
	r.mediaInputProjection = &text
	r.mediaInputCategories = cats
	r.mediaCoverage = append(r.mediaCoverage, MediaInspectionVersion)
	for _, a := range assets {
		r.mediaDigests = append(r.mediaDigests, a.Digest)
		r.mediaInputBytes += int64(len(a.Data))
	}
	return r, nil
}

func normalizeModelOutputWithMedia(ctx context.Context, raw []byte, model Model, processor MediaProcessor) ([]byte, error) {
	if model.Media == nil {
		return normalizeModelOutput(raw, model)
	}
	var response struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int             `json:"index"`
			FinishReason string          `json:"finish_reason"`
			Logprobs     json.RawMessage `json:"logprobs,omitempty"`
			Message      struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
				Refusal *string         `json:"refusal,omitempty"`
				Audio   *struct {
					ID         string `json:"id"`
					Data       string `json:"data"`
					ExpiresAt  int64  `json:"expires_at"`
					Transcript string `json:"transcript"`
				} `json:"audio,omitempty"`
			} `json:"message"`
		} `json:"choices"`
		Usage             *modelUsage `json:"usage,omitempty"`
		SystemFingerprint *string     `json:"system_fingerprint,omitempty"`
		ServiceTier       *string     `json:"service_tier,omitempty"`
	}
	if strictJSON(bytes.NewReader(raw), &response) != nil || response.Object != "chat.completion" || len(response.Choices) != 1 || !response.Usage.valid() {
		return nil, errors.New("unsupported multimodal output")
	}
	expected := model.UpstreamModel
	if expected == "" {
		expected = model.ID
	}
	choice := response.Choices[0]
	if response.Model != expected || choice.Index != 0 || choice.Message.Role != "assistant" || choice.FinishReason != "stop" && choice.FinishReason != "length" || choice.Message.Refusal != nil && *choice.Message.Refusal != "" || len(choice.Logprobs) > 0 && string(choice.Logprobs) != "null" {
		return nil, errors.New("multimodal selector rejected")
	}
	texts := []string{}
	assets := []MediaAsset{}
	var text string
	if json.Unmarshal(choice.Message.Content, &text) == nil && string(choice.Message.Content) != "null" {
		texts = append(texts, text)
	} else if string(choice.Message.Content) != "null" {
		var parts []modelContentPart
		if strictJSON(bytes.NewReader(choice.Message.Content), &parts) != nil || len(parts) > 16 {
			return nil, errors.New("unsupported output parts")
		}
		for i := range parts {
			part := &parts[i]
			if part.Type == "text" && part.ImageURL == nil && part.InputAudio == nil {
				texts = append(texts, part.Text)
				continue
			}
			a, e := mediaPart(part, model.Media)
			if e != nil {
				return nil, e
			}
			assets = append(assets, a)
		}
	}
	if choice.Message.Audio != nil {
		data, e := base64.StdEncoding.Strict().DecodeString(choice.Message.Audio.Data)
		if e != nil {
			return nil, e
		}
		a, e := canonicalMedia("audio/wav", data, model.Media)
		if e != nil {
			return nil, e
		}
		assets = append(assets, a)
		// A provider's transcript is output text too, never an inspection receipt.
		texts = append(texts, choice.Message.Audio.Transcript)
	}
	if len(texts) == 0 && len(assets) == 0 {
		return nil, errors.New("model output content required")
	}
	total := 0
	media := []MediaOutput{}
	for _, a := range assets {
		total += len(a.Data)
		if total > model.Media.MaxTotalBytes || len(media) >= 16 {
			return nil, errors.New("media output budget")
		}
		kind := "image_url"
		if a.MIMEType == "audio/wav" {
			kind = "input_audio"
		}
		media = append(media, MediaOutput{Type: kind, MIMEType: a.MIMEType, Data: base64.StdEncoding.EncodeToString(a.Data), Digest: a.Digest})
	}
	if _, _, e := inspectMediaAssets(ctx, assets, processor); e != nil {
		return nil, e
	}
	return json.Marshal(struct {
		Model        string        `json:"model"`
		Output       string        `json:"output"`
		FinishReason string        `json:"finishReason"`
		Media        []MediaOutput `json:"media,omitempty"`
	}{response.Model, strings.Join(texts, "\n"), choice.FinishReason, media})
}
func ApplyModelOutputInspection(ctx context.Context, r Request, raw []byte, model Model, processor MediaProcessor) (Request, error) {
	if model.Media == nil {
		return r, nil
	}
	var normalized struct {
		Model        string        `json:"model"`
		Output       string        `json:"output"`
		FinishReason string        `json:"finishReason"`
		Media        []MediaOutput `json:"media,omitempty"`
	}
	if strictJSON(bytes.NewReader(raw), &normalized) != nil {
		return r, errors.New("invalid normalized media output")
	}
	assets := []MediaAsset{}
	for _, m := range normalized.Media {
		data, e := base64.StdEncoding.Strict().DecodeString(m.Data)
		if e != nil || mediaHash(data) != m.Digest {
			return r, errors.New("media output digest mismatch")
		}
		policy := *model.Media
		policy.AllowedMIMETypes = append([]string{}, policy.AllowedMIMETypes...)
		if slices.Contains(policy.AllowedMIMETypes, "image/jpeg") && !slices.Contains(policy.AllowedMIMETypes, "image/png") {
			policy.AllowedMIMETypes = append(policy.AllowedMIMETypes, "image/png")
		}
		a, e := canonicalMedia(m.MIMEType, data, &policy)
		if e != nil || a.Digest != m.Digest {
			return r, errors.New("media output not canonical")
		}
		assets = append(assets, a)
	}
	projection, cats, e := inspectMediaAssets(ctx, assets, processor)
	if e != nil {
		return r, e
	}
	text := normalized.Output + "\n" + projection
	r.mediaOutputProjection = &text
	r.mediaOutputCategories = cats
	for _, a := range assets {
		r.mediaDigests = append(r.mediaDigests, a.Digest)
	}
	return r, nil
}
