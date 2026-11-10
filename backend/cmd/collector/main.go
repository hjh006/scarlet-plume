package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	provider := flag.String("provider", "", "postgresql 或 openmetadata")
	source := flag.String("source", "", "客户侧数据源别名")
	service := flag.String("service", "", "OpenMetadata 数据库服务名")
	allowInsecureLoopback := flag.Bool("allow-insecure-loopback", false, "仅本地开发，允许明文连接回环地址")
	var schemas []string
	flag.Func("schema", "要采集的 PostgreSQL schema，可重复指定", func(name string) error {
		schemas = append(schemas, name)
		return nil
	})
	flag.Parse()
	if flag.NArg() != 0 || !collector.ValidSource(*source) {
		return errors.New("请指定有效的 -source 别名，并使用标志传入采集参数")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var assets []collector.Asset
	var err error
	switch *provider {
	case "postgresql":
		if *service != "" {
			return errors.New("-service 只用于 OpenMetadata")
		}
		assets, err = collector.PostgreSQL(ctx, os.Getenv("SCARLET_PG_DSN"), schemas, *allowInsecureLoopback)
	case "openmetadata":
		if len(schemas) != 0 {
			return errors.New("-schema 只用于 PostgreSQL")
		}
		assets, err = collector.OpenMetadata(ctx, os.Getenv("SCARLET_OPENMETADATA_URL"),
			os.Getenv("SCARLET_OPENMETADATA_TOKEN"), *service, *allowInsecureLoopback)
	default:
		return errors.New("-provider 必须为 postgresql 或 openmetadata")
	}
	if err != nil {
		return err
	}
	content, err := collector.EncodeSnapshot(*provider, *source, assets)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(content); err != nil {
		return errors.New("无法写入资产快照")
	}
	return nil
}
