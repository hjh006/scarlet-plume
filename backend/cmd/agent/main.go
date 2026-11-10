package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

type collectSource func(context.Context, *runResult) error

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	provider := flag.String("provider", "", "postgresql、openmetadata 或 feishu")
	source := flag.String("source", "", "任务绑定的资产来源别名")
	service := flag.String("service", "", "OpenMetadata 数据库服务名")
	interval := flag.Duration("interval", 15*time.Minute, "每次采集结束后到下一次采集的间隔")
	once := flag.Bool("once", false, "只采集和提交一次")
	allowLoopback := flag.Bool("allow-insecure-loopback", false, "允许本机开发使用明文回环连接")
	var schemas []string
	flag.Func("schema", "要采集的 PostgreSQL schema，可重复指定", func(name string) error {
		schemas = append(schemas, name)
		return nil
	})
	flag.Parse()
	if flag.NArg() != 0 || *interval < time.Minute {
		return errors.New("请使用采集标志，定期间隔至少为 1 分钟")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := directHTTPClient(15 * time.Second)
	defer client.CloseIdleConnections()
	collect, err := sourceCollector(client, *provider, *source, *service, schemas, *allowLoopback, *once)
	if err != nil {
		return err
	}
	delivery, err := newDelivery(client, os.Getenv("SCARLET_AGENT_URL"), os.Getenv("SCARLET_AGENT_TOKEN"), *allowLoopback)
	if err != nil {
		return err
	}

	for ctx.Err() == nil {
		err := collectAndDeliver(ctx, *provider, collect, delivery)
		if ctx.Err() != nil {
			return nil
		}
		var rejected *deliveryRejected
		if *once || errors.As(err, &rejected) {
			return err
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		if !wait(ctx, *interval) {
			break
		}
	}
	return nil
}

func sourceCollector(client *http.Client, provider, source, service string, schemas []string, allowLoopback, once bool) (collectSource, error) {
	switch provider {
	case "postgresql":
		dsn := os.Getenv("SCARLET_PG_DSN")
		if dsn == "" || len(schemas) == 0 || service != "" || !collector.ValidSource(source) {
			return nil, errors.New("PostgreSQL 任务需要 SCARLET_PG_DSN、-source 和至少一个 -schema")
		}
		return assetCollector(provider, source, func(ctx context.Context) ([]collector.Asset, error) {
			return collector.PostgreSQL(ctx, dsn, schemas, allowLoopback)
		}), nil
	case "openmetadata":
		metadataURL, token := os.Getenv("SCARLET_OPENMETADATA_URL"), os.Getenv("SCARLET_OPENMETADATA_TOKEN")
		if metadataURL == "" || token == "" || service == "" || len(schemas) != 0 || !collector.ValidSource(source) {
			return nil, errors.New("OpenMetadata 任务需要 SCARLET_OPENMETADATA_URL、SCARLET_OPENMETADATA_TOKEN、-source 和 -service")
		}
		return assetCollector(provider, source, func(ctx context.Context) ([]collector.Asset, error) {
			return collector.OpenMetadata(ctx, metadataURL, token, service, allowLoopback)
		}), nil
	case "feishu":
		if source != "" || service != "" || len(schemas) != 0 {
			return nil, errors.New("飞书任务从环境变量读取已绑定的文档和协作者")
		}
		return feishuCollector(client, once)
	default:
		return nil, errors.New("-provider 必须为 postgresql、openmetadata 或 feishu")
	}
}

func assetCollector(provider, source string, collect func(context.Context) ([]collector.Asset, error)) collectSource {
	return func(ctx context.Context, result *runResult) error {
		assets, err := collect(ctx)
		if err != nil {
			return err
		}
		result.Assets, err = collector.EncodeSnapshot(provider, source, assets)
		return err
	}
}

func collectAndDeliver(ctx context.Context, provider string, collect collectSource, delivery deliveryClient) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return errors.New("无法生成采集编号")
	}
	result := runResult{RunID: hex.EncodeToString(id[:]), Provider: provider}
	collection, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := collect(collection, &result)
	if err != nil {
		result.Assets = nil
		result.Feishu = nil
		result.ErrorCode = "collection_failed"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(collection.Err(), context.DeadlineExceeded) {
			result.ErrorCode = "collection_timeout"
		}
	}
	cancel()
	result.ObservedAt = time.Now().UTC()
	if result.Feishu != nil {
		result.ObservedAt = result.Feishu.ObservedAt.UTC()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := delivery.submit(ctx, result); err != nil {
		return err
	}
	if result.ErrorCode != "" {
		return fmt.Errorf("采集失败，平台已记录结果（%s）", result.ErrorCode)
	}
	fmt.Println("采集结果已送达平台")
	return nil
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
