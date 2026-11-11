
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GatewayClaims 定义 JWT 中的用户信息
type GatewayClaims struct {
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

// generateJWT 负责生成 JWT
func generateJWT(
	userID string,
	secret []byte,
	ttl time.Duration,
) (string, error) {

	// 1. 参数检查
	if userID == "" {
		return "", errors.New("user ID cannot be empty")
	}

	if len(secret) < 32 {
		return "", errors.New("JWT secret must contain at least 32 bytes")
	}

	if ttl <= 0 {
		return "", errors.New("JWT TTL must be positive")
	}

	// 2. 获取当前时间
	now := time.Now()

	// 3. 生成 JWT 唯一标识
	jtiBytes := make([]byte, 16)

	if _, err := rand.Read(jtiBytes); err != nil {
		return "", err
	}

	jti := hex.EncodeToString(jtiBytes)

	// 4. 创建 Claims
	claims := &GatewayClaims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "go-gateway-practice",

			Subject: userID,

			Audience: jwt.ClaimStrings{
				"go-gateway",
			},

			IssuedAt: jwt.NewNumericDate(now),

			NotBefore: jwt.NewNumericDate(now),

			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),

			ID: jti,
		},
	}

	// 5. 创建 JWT，使用 HS256
	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		claims,
	)

	// 6. 使用密钥签名
	return token.SignedString(secret)
}

// verifyJWT 负责解析并验证 JWT
func verifyJWT(
	tokenString string,
	secret []byte,
) (*GatewayClaims, error) {

	claims := &GatewayClaims{}

	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (any, error) {
			return secret, nil
		},
		jwt.WithValidMethods([]string{
			jwt.SigningMethodHS256.Alg(),
		}),
		jwt.WithIssuer("go-gateway-practice"),
		jwt.WithAudience("go-gateway"),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)

	if err != nil {
		return nil, err
	}

	if !token.Valid ||
		claims.UserID == "" ||
		claims.Subject != claims.UserID {
		return nil, errors.New("invalid JWT identity")
	}

	return claims, nil
}
