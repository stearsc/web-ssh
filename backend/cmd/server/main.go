package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/maintenance"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
)

func main() {
	filename := flag.String("config", "config.yaml", "YAML 配置文件路径")
	operation := flag.String("operation", "serve", "serve/backup/verify/restore；备份和恢复前必须停止服务")
	recoverySet := flag.String("recovery-set", "", "新建备份目标或已有恢复集目录")
	flag.Parse()
	if *operation == "verify" {
		if *recoverySet == "" {
			slog.Error("请提供恢复集目录")
			os.Exit(1)
		}
		if _, err := maintenance.Verify(context.Background(), *recoverySet); err != nil {
			slog.Error("恢复集校验失败")
			os.Exit(1)
		}
		slog.Info("恢复集校验成功")
		return
	}
	if *operation != "serve" && *operation != "backup" && *operation != "restore" {
		slog.Error("离线操作类型无效")
		os.Exit(1)
	}
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
	if *operation == "serve" {
		err = run(ctx, c, logger)
	} else {
		if *recoverySet == "" {
			logger.Error("请提供恢复集目录")
			os.Exit(1)
		}
		var lock *maintenance.Lock
		lock, err = maintenance.Acquire(c.Database.Path)
		if err == nil {
			if *operation == "backup" {
				var key []byte
				key, err = config.LoadEncryptionKey()
				if err == nil {
					var manifest maintenance.Manifest
					manifest, err = maintenance.Backup(ctx, c.Database.Path, privatekey.Directory, *recoverySet, key)
					if err == nil {
						logger.Info("受控停机备份完成", "program_version", manifest.Program, "master_key_verification", manifest.MasterKeyVerification)
					}
				}
				clear(key)
			} else {
				err = maintenance.Restore(ctx, *recoverySet, c.Database.Path, privatekey.Directory)
				if err == nil {
					logger.Info("恢复完成，请使用恢复集携带的主密钥注入后启动服务")
				}
			}
			lock.Close()
		}
	}

	stop()
	if err != nil {
		if *operation == "serve" {
			logger.Error("服务退出", "error", err)
		} else {
			logger.Error("离线维护失败；请检查进程停止状态、恢复集完整性、目标权限及主密钥，不得启动不完整恢复目标")
		}
		// serve 未完成清理时仍持有无终结器的维护锁，随进程真正退出一并回收。
		os.Exit(1)
	}
}
