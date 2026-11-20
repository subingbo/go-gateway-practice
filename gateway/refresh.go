package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"example.com/go-gateway-practice/internal/redisstore"
)

const (
	defaultRefreshTokenTTL = 7 * 24 * time.Hour
	refreshKeyPrefix       = "gateway:refresh:"
)

var (
	errInvalidRefreshToken = errors.New("invalid refresh token")
	errRefreshTokenReuse   = errors.New("refresh token reuse detected")
)

// RefreshTokenManager 管理 Refresh Token 的生命周期
type RefreshTokenManager struct {
	redis *redisstore.RedisStore
	ttl   time.Duration
}

// RefreshTokenGrant 表示签发给客户端的 Refresh Token
type RefreshTokenGrant struct {
	Token     string
	ExpiresIn int64
}

// RotatedRefreshToken 表示轮换后的 Refresh Token
type RotatedRefreshToken struct {
	UserID    string
	Token     string
	ExpiresIn int64
}

// NewRefreshTokenManager 创建 RefreshTokenManager
func NewRefreshTokenManager(
	client *redisstore.RedisStore,
	ttl time.Duration,
) (*RefreshTokenManager, error) {

	if client == nil {
		return nil, errors.New("redis client cannot be nil")
	}

	if ttl < time.Second {
		return nil, errors.New("refresh token TTL must be at least one second")
	}

	return &RefreshTokenManager{
		redis: client,
		ttl:   ttl,
	}, nil
}

// generateRefreshToken 生成密码学安全的随机 Token
func generateRefreshToken() (string, error) {
	tokenBytes := make([]byte, 32)

	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(tokenBytes), nil
}

// hashRefreshToken 计算 Token 的 SHA-256 哈希
func hashRefreshToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// refreshActiveKey 获取有效 Token 对应的 Redis Key
func refreshActiveKey(hash string) string {
	return refreshKeyPrefix + "active:" + hash
}

// refreshUsedKey 获取已使用 Token 对应的 Redis Key
func refreshUsedKey(hash string) string {
	return refreshKeyPrefix + "used:" + hash
}

// Issue 签发一个新的 Refresh Token
func (m *RefreshTokenManager) Issue(
	ctx context.Context,
	userID string,
) (RefreshTokenGrant, error) {

	if m == nil || m.redis == nil {
		return RefreshTokenGrant{}, errors.New("refresh token manager is not initialized")
	}

	if ctx == nil {
		return RefreshTokenGrant{}, errors.New("context cannot be nil")
	}

	if strings.TrimSpace(userID) == "" {
		return RefreshTokenGrant{}, errors.New("user ID cannot be empty")
	}

	token, err := generateRefreshToken()
	if err != nil {
		return RefreshTokenGrant{}, err
	}

	hash := hashRefreshToken(token)

	script := `
		local created = redis.call(
			"SET",
			KEYS[1],
			ARGV[1],
			"EX",
			ARGV[2],
			"NX"
		)

		if created then
			return 1
		end

		return 0
	`

	result, err := m.redis.Eval(
		ctx,
		script,
		[]string{refreshActiveKey(hash)},
		userID,
		int64(m.ttl/time.Second),
	)
	if err != nil {
		return RefreshTokenGrant{}, err
	}

	created, ok := result.(int64)
	if !ok || created != 1 {
		return RefreshTokenGrant{}, errors.New("failed to store refresh token")
	}

	return RefreshTokenGrant{
		Token:     token,
		ExpiresIn: int64(m.ttl / time.Second),
	}, nil
}

// Rotate 使用旧 Token 换取新的 Refresh Token
func (m *RefreshTokenManager) Rotate(
	ctx context.Context,
	oldToken string,
) (RotatedRefreshToken, error) {

	if m == nil || m.redis == nil {
		return RotatedRefreshToken{}, errors.New("refresh token manager is not initialized")
	}

	if ctx == nil {
		return RotatedRefreshToken{}, errors.New("context cannot be nil")
	}

	if oldToken == "" || oldToken != strings.TrimSpace(oldToken) {
		return RotatedRefreshToken{}, errInvalidRefreshToken
	}

	// 1. 生成新的 Refresh Token
	newToken, err := generateRefreshToken()
	if err != nil {
		return RotatedRefreshToken{}, err
	}

	oldHash := hashRefreshToken(oldToken)
	newHash := hashRefreshToken(newToken)

	// 2. 使用 Lua 原子执行 Token 轮换
	script := `
		local userID = redis.call("GET", KEYS[1])

		-- 旧 Token 不存在
		if not userID then

			-- 如果存在使用记录，说明发生了重放
			if redis.call("EXISTS", KEYS[2]) == 1 then
				return 2
			end

			return 0
		end

		-- 保存新 Token，NX 防止覆盖已有 Key
		local created = redis.call(
			"SET",
			KEYS[3],
			userID,
			"EX",
			ARGV[1],
			"NX"
		)

		if not created then
			return redis.error_reply("new refresh token collision")
		end

		-- 标记旧 Token 已使用
		redis.call(
			"SET",
			KEYS[2],
			userID,
			"EX",
			ARGV[1]
		)

		-- 删除旧 Token
		redis.call("DEL", KEYS[1])

		return userID
	`

	result, err := m.redis.Eval(
		ctx,
		script,
		[]string{
			refreshActiveKey(oldHash),
			refreshUsedKey(oldHash),
			refreshActiveKey(newHash),
		},
		int64(m.ttl/time.Second),
	)
	if err != nil {
		return RotatedRefreshToken{}, err
	}

	// 3. 根据 Lua 返回值判断执行结果
	switch value := result.(type) {
	case string:
		return RotatedRefreshToken{
			UserID:    value,
			Token:     newToken,
			ExpiresIn: int64(m.ttl / time.Second),
		}, nil

	case int64:
		if value == 2 {
			return RotatedRefreshToken{}, errRefreshTokenReuse
		}

		if value == 0 {
			return RotatedRefreshToken{}, errInvalidRefreshToken
		}
	}

	return RotatedRefreshToken{}, fmt.Errorf(
		"unexpected refresh rotation result: %T",
		result,
	)
}

// Revoke 撤销当前有效的 Refresh Token
func (m *RefreshTokenManager) Revoke(
	ctx context.Context,
	token string,
) (bool, error) {

	if m == nil || m.redis == nil {
		return false, errors.New("refresh token manager is not initialized")
	}

	if ctx == nil {
		return false, errors.New("context cannot be nil")
	}

	if token == "" || token != strings.TrimSpace(token) {
		return false, errInvalidRefreshToken
	}

	hash := hashRefreshToken(token)

	result, err := m.redis.Eval(
		ctx,
		`return redis.call("DEL", KEYS[1])`,
		[]string{refreshActiveKey(hash)},
	)
	if err != nil {
		return false, err
	}

	deleted, ok := result.(int64)
	if !ok {
		return false, fmt.Errorf("unexpected revoke result: %T", result)
	}

	return deleted == 1, nil
}
