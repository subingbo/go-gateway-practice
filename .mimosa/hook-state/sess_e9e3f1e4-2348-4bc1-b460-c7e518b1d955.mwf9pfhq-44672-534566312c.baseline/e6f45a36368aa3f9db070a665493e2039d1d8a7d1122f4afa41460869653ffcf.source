
package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func loadOrCreateJWTSecret(path string) ([]byte, error) {

	// 1. 优先读取环境变量
	secretText := strings.TrimSpace(os.Getenv("JWT_SECRET"))

	if secretText != "" {
		return decodeJWTSecret([]byte(secretText))
	}

	// 2. 检查本地密钥文件
	data, err := os.ReadFile(path)

	if err == nil {
		return decodeJWTSecret(data)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// 3. 创建存储目录
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}

	// 4. 自动生成 32 字节随机密钥
	secret := make([]byte, 32)

	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}

	// 5. Base64 编码
	encoded := base64.StdEncoding.EncodeToString(secret)

	// 6. 创建密钥文件，不覆盖已有文件
	file, err := os.OpenFile(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0600,
	)

	// 如果其他进程已经创建了文件，则读取已有密钥
	if errors.Is(err, os.ErrExist) {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		return decodeJWTSecret(data)
	}

	if err != nil {
		return nil, err
	}

	// 7. 写入密钥
	if _, err := file.WriteString(encoded + "\n"); err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}

	if err := file.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}

	return secret, nil
}

// 解码并校验密钥
func decodeJWTSecret(data []byte) ([]byte, error) {

	secret, err := base64.StdEncoding.DecodeString(
		strings.TrimSpace(string(data)),
	)

	if err != nil {
		return nil, fmt.Errorf("invalid JWT secret: %w", err)
	}

	if len(secret) < 32 {
		return nil, errors.New("JWT secret must contain at least 32 bytes")
	}

	return secret, nil
}
