package main

import (
	"context"
	"encoding/json"
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
	targetOnly := flag.Bool("target-only", false, "仅计算检查目标摘要，不调用飞书")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("请通过环境变量提供飞书文档和协作者标识")
	}
	docToken := os.Getenv("SCARLET_FEISHU_DOC_TOKEN")
	memberOpenID := os.Getenv("SCARLET_FEISHU_MEMBER_OPEN_ID")
	if *targetOnly {
		digest, err := collector.FeishuTargetDigest(docToken, memberOpenID)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, digest)
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	observation, err := collector.FeishuACL(ctx, os.Getenv("SCARLET_FEISHU_ACCESS_TOKEN"), docToken, memberOpenID)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(observation)
}
