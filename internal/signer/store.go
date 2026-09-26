package signer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

const maxRecordBytes = 2 << 20

var (
	ErrInvalidStore        = errors.New("测试网 signer 幂等存储无效")
	ErrIdempotencyConflict = errors.New("签名请求幂等键冲突")
)

// FileStore 为单实例测试网 signer 持久化已签名交易，避免进程重启后重复构造付款。
type FileStore struct {
	directory string
	mu        sync.Mutex
}

type storedRecord struct {
	RequestHash       string          `json:"request_hash"`
	TransactionID     string          `json:"transaction_id"`
	SignedTransaction json.RawMessage `json:"signed_transaction"`
}

// NewFileStore 创建或打开权限严格为 0700 的存储目录。
func NewFileStore(directory string) (*FileStore, error) {
	directory = filepath.Clean(directory)
	if directory == "." || !filepath.IsAbs(directory) {
		return nil, ErrInvalidStore
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("创建 signer 存储目录: %w", ErrInvalidStore)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidStore
	}
	return &FileStore{directory: directory}, nil
}

// Resolve 返回已有结果，或将首次签名结果原子持久化后返回。
func (store *FileStore) Resolve(
	ctx context.Context,
	requestID string,
	requestHash string,
	create func() (tron.SignedTransaction, error),
) (tron.SignedTransaction, error) {
	if store == nil || !identity.ValidUUID(requestID) || len(requestHash) != 64 || create == nil {
		return tron.SignedTransaction{}, ErrInvalidStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return tron.SignedTransaction{}, err
	}
	path := filepath.Join(store.directory, requestID+".json")
	transaction, exists, err := store.load(path, requestHash)
	if err != nil || exists {
		return transaction, err
	}
	transaction, err = create()
	if err != nil {
		return tron.SignedTransaction{}, err
	}
	if err := tron.ValidateSignedTransaction(transaction); err != nil {
		return tron.SignedTransaction{}, ErrInvalidStore
	}
	record, err := json.Marshal(storedRecord{
		RequestHash: requestHash, TransactionID: transaction.ID,
		SignedTransaction: json.RawMessage(transaction.Payload),
	})
	if err != nil || len(record) > maxRecordBytes {
		return tron.SignedTransaction{}, ErrInvalidStore
	}
	temporary, err := os.CreateTemp(store.directory, ".signing-*.tmp")
	if err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("创建 signer 临时记录: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return tron.SignedTransaction{}, fmt.Errorf("设置 signer 记录权限: %w", err)
	}
	if _, err := temporary.Write(record); err != nil {
		temporary.Close()
		return tron.SignedTransaction{}, fmt.Errorf("写入 signer 记录: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return tron.SignedTransaction{}, fmt.Errorf("同步 signer 记录: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("关闭 signer 记录: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return store.loadExisting(path, requestHash)
		}
		return tron.SignedTransaction{}, fmt.Errorf("提交 signer 记录: %w", err)
	}
	directory, err := os.Open(store.directory)
	if err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("打开 signer 存储目录: %w", err)
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return tron.SignedTransaction{}, fmt.Errorf("同步 signer 存储目录: %w", errors.Join(err, closeErr))
	}
	return transaction, nil
}

func (store *FileStore) loadExisting(path string, requestHash string) (tron.SignedTransaction, error) {
	transaction, exists, err := store.load(path, requestHash)
	if err != nil {
		return tron.SignedTransaction{}, err
	}
	if !exists {
		return tron.SignedTransaction{}, ErrInvalidStore
	}
	return transaction, nil
}

func (store *FileStore) load(path string, requestHash string) (tron.SignedTransaction, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return tron.SignedTransaction{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxRecordBytes {
		return tron.SignedTransaction{}, false, ErrInvalidStore
	}
	file, err := os.Open(path)
	if err != nil {
		return tron.SignedTransaction{}, false, ErrInvalidStore
	}
	defer file.Close()
	var record storedRecord
	decoder := json.NewDecoder(io.LimitReader(file, maxRecordBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || ensureEOF(decoder) != nil {
		return tron.SignedTransaction{}, false, ErrInvalidStore
	}
	if !bytes.Equal([]byte(record.RequestHash), []byte(requestHash)) {
		return tron.SignedTransaction{}, false, ErrIdempotencyConflict
	}
	transaction := tron.SignedTransaction{
		ID: record.TransactionID, Payload: bytes.Clone(record.SignedTransaction),
	}
	if err := tron.ValidateSignedTransaction(transaction); err != nil {
		return tron.SignedTransaction{}, false, ErrInvalidStore
	}
	return transaction, true, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidStore
	}
	return nil
}
