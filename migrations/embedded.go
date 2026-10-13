// Package migrations 包含与当前程序版本配套的数据库迁移。
package migrations

import (
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Files 保存版本化 SQL 迁移文件。
//
//go:embed *.sql
var Files embed.FS

// LatestVersion 返回嵌入迁移的最新连续版本，并校验每个版本同时存在 up/down 文件。
func LatestVersion() (uint, error) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		return 0, fmt.Errorf("读取嵌入迁移: %w", err)
	}
	type directions struct {
		up   bool
		down bool
	}
	versions := make(map[uint]directions)
	var latest uint
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < 11 {
			continue
		}
		direction := ""
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			direction = "up"
		case strings.HasSuffix(name, ".down.sql"):
			direction = "down"
		default:
			continue
		}
		separator := strings.IndexByte(name, '_')
		if separator <= 0 {
			return 0, fmt.Errorf("迁移文件名无效: %s", name)
		}
		parsed, parseErr := strconv.ParseUint(name[:separator], 10, 32)
		if parseErr != nil || parsed == 0 {
			return 0, fmt.Errorf("迁移版本无效: %s", name)
		}
		version := uint(parsed)
		state := versions[version]
		if direction == "up" {
			state.up = true
		} else {
			state.down = true
		}
		versions[version] = state
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, errors.New("没有嵌入数据库迁移")
	}
	for version := uint(1); version <= latest; version++ {
		state, exists := versions[version]
		if !exists || !state.up || !state.down {
			return 0, fmt.Errorf("数据库迁移版本 %d 不连续或缺少 up/down 文件", version)
		}
	}
	return latest, nil
}
