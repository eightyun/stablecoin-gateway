package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		if err := validate(options{version: "v0.1.0-rc.1", target: target}); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range []string{"", "../release", "-version", "a/b", "a b", "a\n", strings.Repeat("v", 81)} {
		if err := validate(options{version: version, target: "linux/amd64"}); err == nil {
			t.Fatalf("接受非法版本 %q", version)
		}
	}
	if err := validate(options{version: "v0.1.0", target: "windows/amd64"}); err == nil {
		t.Fatal("接受未支持平台")
	}
}

func TestArchiveDeterministicAndPreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "command")
	if err := os.WriteFile(source, []byte("executable"), 0600); err != nil {
		t.Fatal(err)
	}
	entries := []entry{{name: "bin/gateway-api", path: source, mode: 0755}, {name: "README.md", path: source, mode: 0644}}
	timestamp := time.Unix(1700000000, 0).UTC()
	first, second := filepath.Join(dir, "first.tar.gz"), filepath.Join(dir, "second.tar.gz")
	if err := writeArchive(first, "release", entries, timestamp); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(second, "release", entries, timestamp); err != nil {
		t.Fatal(err)
	}
	left, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.ReadFile(second)
	if err != nil || !bytes.Equal(left, right) {
		t.Fatalf("相同输入未得到相同归档: %v", err)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(left))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	for _, expected := range []struct {
		name string
		mode int64
	}{{"release/README.md", 0644}, {"release/bin/gateway-api", 0755}} {
		header, err := archive.Next()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(archive)
		if err != nil || string(content) != "executable" || header.Name != expected.name || header.Mode != expected.mode || !header.ModTime.Equal(timestamp) || header.Uid != 0 || header.Gid != 0 {
			t.Fatalf("归档内容不符: header=%+v error=%v", header, err)
		}
	}
	if _, err := archive.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("归档包含额外文件: %v", err)
	}
	if err := writeArchive(first, "release", entries, timestamp); !errors.Is(err, os.ErrExist) {
		t.Fatalf("已有产物未受保护: %v", err)
	}
}

func TestArchiveRejectsSymlinksAndTraversal(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(source, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	for _, item := range []entry{{name: "README.md", path: link}, {name: "../secret", path: source}, {name: "/secret", path: source}, {name: `bin\secret`, path: source}} {
		output := filepath.Join(dir, "rejected.tar.gz")
		if err := writeArchive(output, "release", []entry{item}, time.Unix(1, 0)); err == nil {
			t.Fatalf("接受非法条目: %+v", item)
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("失败后留下不完整产物: %v", err)
		}
	}
}

func TestPublicEntriesExcludeWorkspaceSecrets(t *testing.T) {
	root, binDir := t.TempDir(), t.TempDir()
	for _, name := range []string{".env", "wallet.key", "private.pem"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(binDir, "gateway-api"), []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	entries, err := publicEntries(root, binDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 15 {
		t.Fatalf("公共文件白名单发生非预期变化: %d", len(entries))
	}
	for _, item := range entries {
		if strings.HasPrefix(item.name, "docs/") || (strings.HasPrefix(item.name, ".env") && item.name != ".env.example") || strings.Contains(item.name, "private") || strings.HasSuffix(item.name, ".key") {
			t.Fatalf("归档包含私有文件: %s", item.name)
		}
	}
	if err := os.WriteFile(filepath.Join(binDir, "unexpected"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := publicEntries(root, binDir); err == nil {
		t.Fatal("接受非预期的命令产物")
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(path)
	if err != nil || digest != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA-256 不符: %q %v", digest, err)
	}
}

func TestReadSourceTracksCommitAndDirtyState(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=Release Test", "GIT_AUTHOR_EMAIL=release@example.invalid", "GIT_COMMITTER_NAME=Release Test", "GIT_COMMITTER_EMAIL=release@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "--quiet")
	git("-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "initial")
	source, err := readSource(dir)
	if err != nil || len(source.Commit) != 40 || source.Dirty || source.CommitTime.Format(time.RFC3339) != "2026-01-01T00:00:00Z" {
		t.Fatalf("提交信息不符: %+v %v", source, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	dirty, err := readSource(dir)
	if err != nil || !dirty.Dirty || dirty.Commit != source.Commit {
		t.Fatalf("未检测未提交源码: %+v %v", dirty, err)
	}
}
