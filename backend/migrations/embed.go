package migrations

import "embed"

// Files 只包含按版本执行的 SQL，不从运行目录加载可变迁移文件。
//
//go:embed *.sql
var Files embed.FS
