package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/stearsc/web-ssh/backend/internal/config"
)

func main() {
	filename := flag.String("config", "config.yaml", "YAML 配置文件路径")
	flag.Parse()
	c, err := config.Load(*filename)
	if err != nil {
		slog.Error("配置校验失败", "error", err)
		os.Exit(1)
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.Logging.Level)); err != nil {
		slog.Error("日志配置非法")
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = run(ctx, c, logger)
	stop()
	if err != nil {
		logger.Error("服务退出", "error", err)
		os.Exit(1)
	}
}
