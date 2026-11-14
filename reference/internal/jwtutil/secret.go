package jwtutil

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateSecret 按“环境变量 → 文件 → 自动生成”的顺序加载 JWT 密钥。
//
// 自动生成时使用 O_CREATE|O_EXCL 和 0600 权限，多个进程同时启动也不会
// 互相覆盖。
func LoadOrCreateSecret(path string) ([]byte, error) {
	if value := os.Getenv("JWT_SECRET"); value != "" {
		return decodeSecret([]byte(value))
	}

	data, err := os.ReadFile(path)
	if err == nil {
		return decodeSecret(data)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read JWT secret: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create secret directory: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate JWT secret: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(raw)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, readErr
			}
			return decodeSecret(data)
		}

		return nil, fmt.Errorf("create JWT secret file: %w", err)
	}

	if _, err := file.WriteString(encoded + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write JWT secret file: %w", err)
	}

	if err := file.Close(); err != nil {
		return nil, err
	}

	return raw, nil
}

func decodeSecret(data []byte) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("JWT secret must be base64: %w", err)
	}

	if len(decoded) < 32 {
		return nil, errors.New("JWT secret must contain at least 32 bytes")
	}

	return decoded, nil
}
