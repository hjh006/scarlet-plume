package aisecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type CommandMediaOptions struct{ Tesseract, Whisper, WhisperModel, WhisperModelSHA256, Languages string }
type mediaOutputLimit struct{ bytes.Buffer }

func (b *mediaOutputLimit) Write(raw []byte) (int, error) {
	if b.Len()+len(raw) > 2<<20 {
		return 0, errors.New("inspection output budget")
	}
	return b.Buffer.Write(raw)
}

func NewCommandMediaProcessor(o CommandMediaOptions) (MediaProcessor, error) {
	if o.Languages == "" {
		o.Languages = "eng"
	}
	if !regexp.MustCompile(`^[a-z_]+(\+[a-z_]+)*$`).MatchString(o.Languages) || o.Tesseract == "" && o.Whisper == "" {
		return nil, errors.New("media inspection commands required")
	}
	for _, path := range []string{o.Tesseract, o.Whisper} {
		if path != "" {
			if !filepath.IsAbs(path) {
				return nil, errors.New("inspection binary must use an absolute path")
			}
			if _, e := exec.LookPath(path); e != nil {
				return nil, errors.New("inspection binary unavailable")
			}
		}
	}
	if o.Whisper != "" {
		if !filepath.IsAbs(o.WhisperModel) || !digest.MatchString(o.WhisperModelSHA256) {
			return nil, errors.New("pinned speech model required")
		}
		if e := verifyWhisperModel(o); e != nil {
			return nil, e
		}
	}
	return func(ctx context.Context, a MediaAsset) (MediaReport, error) {
		r := MediaReport{Digest: a.Digest, MIMEType: a.MIMEType, InspectionVersion: MediaInspectionVersion}
		if mediaHash(a.Data) != a.Digest || len(a.Data) > 4<<20 {
			return r, errors.New("inspection media binding invalid")
		}
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		dir, e := os.MkdirTemp("", "scarlet-media-")
		if e != nil {
			return r, e
		}
		defer os.RemoveAll(dir)
		ext := ".png"
		if a.MIMEType == "image/jpeg" {
			ext = ".jpg"
		}
		if a.MIMEType == "audio/wav" {
			ext = ".wav"
		}
		path := filepath.Join(dir, "input"+ext)
		if e = os.WriteFile(path, a.Data, 0600); e != nil {
			return r, e
		}
		var command *exec.Cmd
		var stdout mediaOutputLimit
		if strings.HasPrefix(a.MIMEType, "image/") {
			if o.Tesseract == "" {
				return r, errors.New("OCR unavailable")
			}
			command = exec.CommandContext(ctx, o.Tesseract, path, "stdout", "-l", o.Languages, "--psm", "6", "tsv")
		} else if a.MIMEType == "audio/wav" {
			if o.Whisper == "" {
				return r, errors.New("ASR unavailable")
			}
			if e := verifyWhisperModel(o); e != nil {
				return r, e
			}
			command = exec.CommandContext(ctx, o.Whisper, "-m", o.WhisperModel, "-f", path, "-ojf", "-of", filepath.Join(dir, "transcript"), "-np", "-nt", "-ng", "-t", "2", "-l", "auto")
		} else {
			return r, errors.New("unsupported media inspection")
		}
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "OMP_THREAD_LIMIT=2", "LC_ALL=C"}
		command.Stdout = &stdout
		command.Stderr = io.Discard
		if e = command.Run(); e != nil {
			return r, errors.New("media recognition failed or timed out")
		}
		if strings.HasPrefix(a.MIMEType, "image/") {
			reader := csv.NewReader(bytes.NewReader(stdout.Bytes()))
			reader.Comma = '\t'
			reader.FieldsPerRecord = -1
			reader.LazyQuotes = true
			var text []string
			minimum, recognized := 100.0, false
			for {
				row, e := reader.Read()
				if e == io.EOF {
					break
				}
				if e != nil || len(row) < 12 {
					return r, errors.New("OCR result incomplete")
				}
				if row[0] != "5" {
					continue
				}
				confidence, e := strconv.ParseFloat(row[10], 64)
				if e != nil || math.IsNaN(confidence) || math.IsInf(confidence, 0) {
					return r, errors.New("OCR confidence invalid")
				}
				word := strings.TrimSpace(row[11])
				if word == "" {
					continue
				}
				if confidence < 0 || confidence > 100 {
					return r, errors.New("OCR confidence invalid")
				}
				text = append(text, word)
				minimum, recognized = min(minimum, confidence), true
			}
			r.Text = strings.Join(text, " ")
			if recognized {
				r.Confidence = minimum
			}
		} else {
			file, e := os.Open(filepath.Join(dir, "transcript.json"))
			if e != nil {
				return r, errors.New("speech result unavailable")
			}
			raw, e := io.ReadAll(io.LimitReader(file, (2<<20)+1))
			_ = file.Close()
			if e != nil || len(raw) > 2<<20 {
				return r, errors.New("speech result incomplete")
			}
			var result struct {
				Transcription []struct {
					Text   string `json:"text"`
					Tokens []struct {
						Text string  `json:"text"`
						P    float64 `json:"p"`
					} `json:"tokens"`
				} `json:"transcription"`
			}
			if json.Unmarshal(raw, &result) != nil {
				return r, errors.New("speech result invalid")
			}
			var text []string
			minimum, recognized := 100.0, false
			for _, segment := range result.Transcription {
				text = append(text, segment.Text)
				for _, token := range segment.Tokens {
					if math.IsNaN(token.P) || math.IsInf(token.P, 0) || token.P < 0 || token.P > 1 {
						return r, errors.New("speech confidence invalid")
					}
					text := strings.TrimSpace(token.Text)
					if text == "" || strings.HasPrefix(text, "[_") && strings.HasSuffix(text, "]") {
						continue
					}
					minimum, recognized = min(minimum, 100*token.P), true
				}
			}
			r.Text = strings.TrimSpace(strings.Join(text, " "))
			if recognized {
				r.Confidence = minimum
			}
		}
		if !bounded(r.Text, 1<<20) {
			return r, errors.New("recognition text over budget")
		}
		r.Complete = true
		return r, nil
	}, nil
}
func verifyWhisperModel(o CommandMediaOptions) error {
	f, e := os.Open(o.WhisperModel)
	if e != nil {
		return errors.New("speech model unavailable")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() < 1024 || st.Size() > 2<<30 {
		return errors.New("speech model invalid")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return errors.New("speech model incomplete")
	}
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(h.Sum(nil))), []byte(o.WhisperModelSHA256)) != 1 {
		return errors.New("speech model integrity mismatch")
	}
	return nil
}

func NewHTTPMediaProcessor(endpoint, token string, allowLoopback bool) (MediaProcessor, error) {
	return NewHTTPMediaProcessorWithCA(endpoint, token, allowLoopback, nil)
}

// NewHTTPMediaProcessorWithCA appends a deployment-approved private CA only to
// the scanner client. Controller/provider clients and system roots are unchanged.
func NewHTTPMediaProcessorWithCA(endpoint, token string, allowLoopback bool, caPEM []byte) (MediaProcessor, error) {
	if !validTarget(endpoint, allowLoopback) || len(token) < 32 || len(token) > 512 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid media inspector endpoint or credential")
	}
	var roots *x509.CertPool
	if len(caPEM) > 0 {
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("scanner system trust unavailable")
		}
		if len(caPEM) > 1<<20 || !roots.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("scanner CA invalid")
		}
	}
	client := &http.Client{Timeout: 50 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DisableCompression: true, ResponseHeaderTimeout: 45 * time.Second, MaxResponseHeaderBytes: 16384, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}
	return func(ctx context.Context, a MediaAsset) (MediaReport, error) {
		var out MediaReport
		raw, _ := json.Marshal(a)
		request, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
		if e != nil {
			return out, e
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response, e := client.Do(request)
		if e != nil {
			return out, errors.New("media inspector unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "" {
			return out, errors.New("media inspector rejected content")
		}
		raw, e = io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if e != nil || len(raw) > 2<<20 || strictJSON(bytes.NewReader(raw), &out) != nil || !validMediaInspectionReport(out, a) {
			return out, errors.New("media inspection receipt invalid")
		}
		return out, nil
	}, nil
}

func validMediaInspectionReport(out MediaReport, a MediaAsset) bool {
	return out.Complete && out.Digest == a.Digest && out.MIMEType == a.MIMEType && out.InspectionVersion == MediaInspectionVersion && bounded(out.Text, 1<<20) && !math.IsNaN(out.Confidence) && !math.IsInf(out.Confidence, 0) && out.Confidence >= 0 && out.Confidence <= 100
}

// NewMediaInspectionHandler is for a dedicated private scanner service, never
// mounted on the public gateway. It accepts only authenticated bounded bytes.
func NewMediaInspectionHandler(processor MediaProcessor, token string) (http.Handler, error) {
	if processor == nil || len(token) < 32 || len(token) > 512 {
		return nil, errors.New("scanner processor and credential required")
	}
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/inspect" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Content-Encoding") != "" {
			respond(w, 400, map[string]string{"code": "inspection_protocol_invalid"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			respond(w, 401, map[string]string{"code": "inspection_authentication_required"})
			return
		}
		if r.Context().Err() != nil {
			respond(w, http.StatusRequestTimeout, map[string]string{"code": "inspection_cancelled"})
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			respond(w, http.StatusTooManyRequests, map[string]string{"code": "inspection_busy"})
			return
		}
		var asset MediaAsset
		if strictJSON(http.MaxBytesReader(w, r.Body, 6<<20), &asset) != nil || len(asset.Data) > 4<<20 || asset.Digest != mediaHash(asset.Data) {
			respond(w, 400, map[string]string{"code": "inspection_media_invalid"})
			return
		}
		policy := &MediaPolicy{AllowedMIMETypes: []string{"image/png", "image/jpeg", "audio/wav"}, MaxAssetBytes: 4 << 20, MaxTotalBytes: 8 << 20, MaxPixels: 16000000, InspectionVersion: MediaInspectionVersion}
		canonical, e := canonicalMedia(asset.MIMEType, asset.Data, policy)
		if e != nil || canonical.Digest != asset.Digest {
			respond(w, 400, map[string]string{"code": "inspection_media_not_canonical"})
			return
		}
		if r.Context().Err() != nil {
			respond(w, http.StatusRequestTimeout, map[string]string{"code": "inspection_cancelled"})
			return
		}
		out, e := processor(r.Context(), asset)
		if e != nil {
			respond(w, 422, map[string]string{"code": "inspection_incomplete"})
			return
		}
		if !validMediaInspectionReport(out, asset) {
			respond(w, 422, map[string]string{"code": "inspection_receipt_invalid"})
			return
		}
		respond(w, 200, out)
	}), nil
}
