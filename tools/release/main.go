// release 构建可追溯、无运行凭据的离线分发包。
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type options struct {
	version    string
	target     string
	output     string
	allowDirty bool
}

type sourceInfo struct {
	Commit     string    `json:"commit"`
	CommitTime time.Time `json:"commit_time"`
	Dirty      bool      `json:"dirty"`
}

type fileInfo struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Version string `json:"version"`
	sourceInfo
	Target    string     `json:"target"`
	GoVersion string     `json:"go_version"`
	Files     []fileInfo `json:"files"`
}

type entry struct {
	name string
	path string
	mode int64
}

func main() {
	var opts options
	flag.StringVar(&opts.version, "version", "", "版本标识，默认 snapshot-<commit>")
	flag.StringVar(&opts.target, "target", runtime.GOOS+"/"+runtime.GOARCH, "linux/amd64、linux/arm64、darwin/amd64 或 darwin/arm64")
	flag.StringVar(&opts.output, "output", "build/release", "产物目录，拒绝覆盖已有文件")
	flag.BoolVar(&opts.allowDirty, "allow-dirty", false, "仅本地验证：允许未提交的源码，产物标注 dirty")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "不接受位置参数")
		os.Exit(1)
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validate(opts options) error {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`).MatchString(opts.version) {
		return errors.New("版本标识必须为 1-80 位字母、数字、点、下划线或连字符，并以字母或数字开头")
	}
	switch opts.target {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64":
	default:
		return errors.New("不支持的目标平台")
	}
	return nil
}

func gitOutput(root string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("读取 Git 信息: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func readSource(root string) (sourceInfo, error) {
	commit, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return sourceInfo{}, err
	}
	timestamp, err := gitOutput(root, "show", "-s", "--format=%ct", "HEAD")
	if err != nil {
		return sourceInfo{}, err
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return sourceInfo{}, fmt.Errorf("解析提交时间: %w", err)
	}
	status, err := gitOutput(root, "status", "--porcelain", "--untracked-files=all")
	return sourceInfo{Commit: commit, CommitTime: time.Unix(seconds, 0).UTC(), Dirty: status != ""}, err
}

func run(opts options) error {
	root, err := gitOutput(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("发布工具必须在项目仓库中运行: %w", err)
	}
	source, err := readSource(root)
	if err != nil {
		return err
	}
	if source.Dirty && !opts.allowDirty {
		return errors.New("源码未提交，拒绝发布构建；本地验证可显式使用 -allow-dirty")
	}
	if opts.version == "" {
		opts.version = "snapshot-" + source.Commit[:12]
	}
	if err := validate(opts); err != nil {
		return err
	}
	name := "stablecoin-gateway_" + opts.version + "_" + strings.ReplaceAll(opts.target, "/", "_")
	if source.Dirty {
		name += "_dirty"
	}
	outputDir, err := filepath.Abs(opts.output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	archivePath := filepath.Join(outputDir, name+".tar.gz")
	checksumPath := archivePath + ".sha256"
	for _, path := range []string{archivePath, checksumPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("产物路径已存在或不可用: %s", path)
		}
	}
	stage, err := os.MkdirTemp("", "stablecoin-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	binDir := filepath.Join(stage, "bin")
	if err := os.Mkdir(binDir, 0755); err != nil {
		return err
	}
	target := strings.Split(opts.target, "/")
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=true", "-ldflags=-s -w -buildid=", "-o", binDir+string(os.PathSeparator), "./cmd/...")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target[0], "GOARCH="+target[1], "GOAMD64=v1", "GOARM64=v8.0", "GOFLAGS=", "GOWORK=off", "GOEXPERIMENT=")
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("构建命令: %w", err)
	}
	entries, err := publicEntries(root, binDir)
	if err != nil {
		return err
	}
	report := manifest{Version: opts.version, sourceInfo: source, Target: opts.target, GoVersion: runtime.Version()}
	for _, item := range entries {
		digest, err := hashFile(item.path)
		if err != nil {
			return err
		}
		report.Files = append(report.Files, fileInfo{Path: item.name, SHA256: digest})
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(stage, "manifest.json")
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0644); err != nil {
		return err
	}
	entries = append(entries, entry{name: "manifest.json", path: manifestPath, mode: 0644})
	current, err := readSource(root)
	if err != nil {
		return err
	}
	if current != source {
		return errors.New("构建期间 Git 提交或工作区状态发生变化，请重新构建")
	}
	if err := writeArchive(archivePath, name, entries, source.CommitTime); err != nil {
		return err
	}
	digest, err := hashFile(archivePath)
	if err != nil {
		return err
	}
	if err := writeExclusive(checksumPath, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "%s  %s\n", digest, filepath.Base(archivePath))
		return err
	}); err != nil {
		return err
	}
	fmt.Println(archivePath)
	fmt.Println(checksumPath)
	return nil
}

func publicEntries(root, binDir string) ([]entry, error) {
	// 仅打包固定公开路径，不遍历工作区，避免带入 docs、环境文件、签名状态或私钥。
	paths := []string{"README.md", "README.en.md", "LICENSE", "SECURITY.md", ".env.example"}
	for _, path := range []string{
		"configs/prometheus/alerts.yml",
		"configs/grafana/dashboards/stablecoin-gateway.json",
		"configs/grafana/provisioning/dashboards/stablecoin-gateway.yml",
		"configs/systemd/stablecoin-gateway@.service",
		"configs/systemd/stablecoin-gateway-controls@.service",
		"configs/systemd/stablecoin-gateway-controls@.timer",
		"configs/systemd/api.env.example",
		"configs/systemd/monitor.env.example",
		"configs/systemd/controls.env.example",
	} {
		paths = append(paths, path)
	}
	var entries []entry
	for _, path := range paths {
		entries = append(entries, entry{name: path, path: filepath.Join(root, filepath.FromSlash(path)), mode: 0644})
	}
	binaries, err := os.ReadDir(binDir)
	if err != nil {
		return nil, err
	}
	if len(binaries) == 0 {
		return nil, errors.New("构建没有产生可执行文件")
	}
	for _, binary := range binaries {
		if binary.IsDir() || !strings.HasPrefix(binary.Name(), "gateway-") {
			return nil, fmt.Errorf("非预期的命令产物: %s", binary.Name())
		}
		entries = append(entries, entry{name: "bin/" + binary.Name(), path: filepath.Join(binDir, binary.Name()), mode: 0755})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeExclusive(path string, write func(io.Writer) error) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	return write(file)
}

func writeArchive(path, prefix string, entries []entry, timestamp time.Time) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return writeExclusive(path, func(output io.Writer) (err error) {
		compressed := gzip.NewWriter(output)
		defer func() { err = errors.Join(err, compressed.Close()) }()
		archive := tar.NewWriter(compressed)
		defer func() { err = errors.Join(err, archive.Close()) }()
		for _, item := range entries {
			info, err := os.Lstat(item.path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || !filepath.IsLocal(item.name) || strings.Contains(item.name, "\\") {
				return fmt.Errorf("拒绝归档非普通文件或非本地路径: %s", item.name)
			}
			if err := archive.WriteHeader(&tar.Header{Name: prefix + "/" + item.name, Mode: item.mode, Size: info.Size(), ModTime: timestamp, Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if err := copyFile(archive, item.path); err != nil {
				return err
			}
		}
		return nil
	})
}

func copyFile(output io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(output, file)
	return err
}
