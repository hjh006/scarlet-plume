// discovery-agent runs in the customer's environment; database credentials never enter platform configuration.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func main() {
	interval := flag.Duration("poll", 15*time.Second, "任务轮询周期（生产最少5s，本地Docker最少2s）")
	localDocker := flag.Bool("local-docker", false, "仅连接固定本地Docker平台与样例数据库")
	insecure := flag.Bool("allow-insecure-loopback", false, "仅允许回环明文开发连接")
	once := flag.Bool("once", false, "只领取一次任务")
	pdf := flag.Bool("file-pdf", false, "本地文件显式启用Poppler文本提取")
	ocr := flag.Bool("file-ocr", false, "本地文件显式启用Tesseract图片OCR")
	flag.Parse()
	minimum := 5 * time.Second
	if *localDocker {
		minimum = 2 * time.Second
	}
	if *interval < minimum {
		log.Fatal("轮询周期低于当前模式允许的最短周期")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	endpoint, token := os.Getenv("SCARLET_URL"), os.Getenv("SCARLET_DISCOVERY_TOKEN")
	if len(token) != 43 {
		log.Fatal("请配置SCARLET_DISCOVERY_TOKEN")
	}
	if e := validatePlatform(endpoint, *insecure, *localDocker); e != nil {
		log.Fatal(e)
	}
	var e error

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	post := func(path string, input, output any) error {
		body, e := json.Marshal(input)
		if e != nil {
			return e
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body))
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e != nil {
			return errors.New("平台连接失败")
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("平台拒绝任务请求(%d)", resp.StatusCode)
		}
		decoder := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024))
		return decoder.Decode(output)
	}
	for {
		var response struct {
			Claim *store.DiscoveryClaim `json:"claim"`
		}
		if e = post("/api/v1/discovery-agent/claim", struct{}{}, &response); e != nil {
			log.Printf("任务领取失败：%v", e)
		} else if response.Claim != nil {
			claim := response.Claim
			dsn := os.Getenv("SCARLET_DISCOVERY_DSN")
			if claim.Config.Provider == "openmetadata" {
				dsn = os.Getenv("SCARLET_OPENMETADATA_URL")
			}
			var result collector.DiscoveryResult
			var collectErr error
			if claim.Config.Provider == "local_files" {
				result, collectErr = collector.DiscoverLocalFiles(ctx, claim.Config, os.Getenv("SCARLET_FILE_ROOT"), collector.FileScanOptions{FingerprintKey: os.Getenv("SCARLET_VALUE_KEY_HEX"), FingerprintKeyID: os.Getenv("SCARLET_VALUE_KEY_ID"), FingerprintScope: os.Getenv("SCARLET_VALUE_SCOPE"), DigestKey: []byte(token), PDF: *pdf, OCR: *ocr, OCRLanguages: os.Getenv("SCARLET_FILE_OCR_LANGUAGES")})
			} else {
				result, collectErr = collector.DiscoverWithConnection(ctx, claim.Config, collector.DiscoveryConnection{DSN: dsn, Token: os.Getenv("SCARLET_OPENMETADATA_TOKEN"), AllowInsecureLoopback: *insecure, LocalDocker: *localDocker})
			}
			submission := store.DiscoverySubmission{SourceID: claim.SourceID, Revision: claim.Revision, RunRef: claim.RunRef, Status: "success", ObservedAt: time.Now().UTC(), Result: result}
			if collectErr != nil {
				submission.Status = "error"
				submission.ErrorCode = "collection_failed"
				log.Printf("来源%s采集失败（请核对连接、授权及范围）", claim.Source)
			}
			var receipt store.DiscoveryRun
			if e = post("/api/v1/discovery-agent/runs", submission, &receipt); e != nil {
				log.Printf("结果提交失败：%v", e)
			} else {
				log.Printf("任务%s：%s，表%d", claim.Source, receipt.Status, len(result.Tables))
			}
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*interval):
		}
	}
}
