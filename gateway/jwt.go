
package main

import (
    "errors"

    "github.com/golang-jwt/jwt/v5"
)

// GatewayClaims 定义 JWT 中的用户信息
type GatewayClaims struct {
    UserID string `json:"user_id"`
    jwt.RegisteredClaims
}

// verifyJWT 负责解析并验证 JWT
func verifyJWT(tokenString string, secret []byte) (*GatewayClaims, error) {

    // 1. 创建 Claims，用于接收解析出的 Payload
    claims := &GatewayClaims{}

    // 2. 解析 Token 并进行验证
    token, err := jwt.ParseWithClaims(
        tokenString,
        claims,

        // 通过回调提供验证签名所需的密钥
        func(token *jwt.Token) (any, error) {
            return secret, nil
        },

        // 只接受 HS256
        jwt.WithValidMethods([]string{
            jwt.SigningMethodHS256.Alg(),
        }),

        // 验证签发者
        jwt.WithIssuer("go-gateway-practice"),

        // 验证接收方
        jwt.WithAudience("go-gateway"),

        // 要求 Token 必须有过期时间
        jwt.WithExpirationRequired(),

        // 如果有签发时间，检查它是否合法
        jwt.WithIssuedAt(),
    )

    if err != nil {
        return nil, err
    }

    // 3. 确认身份有效
    if !token.Valid ||
        claims.UserID == "" ||
        claims.Subject != claims.UserID {
        return nil, errors.New("invalid JWT identity")
    }

    return claims, nil
}
