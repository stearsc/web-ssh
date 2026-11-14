package web

import (
	"embed"
	"io/fs"
)

// 构建前必须先构建前端，不使用占位页掩盖遗漏的生产资源。
//
//go:embed dist
var embedded embed.FS

func Assets() (fs.FS, error) { return fs.Sub(embedded, "dist") }
