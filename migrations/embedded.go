// Package migrations 包含与当前程序版本配套的数据库迁移。
package migrations

import "embed"

// Files 保存版本化 SQL 迁移文件。
//
//go:embed *.sql
var Files embed.FS
