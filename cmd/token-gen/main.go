package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func generateToken(secret []byte) (string, error) {
	// 1. 获取当前时间
	now := time.Now()

	// 2. 创建 JWT Payload
	claims := jwt.MapClaims{
		"user_id": "1001",
		"sub":     "1001",
		"iss":     "go-gateway-practice",
		"aud":     "go-gateway",
		"iat":     now.Unix(),
		"exp":     now.Add(15 * time.Minute).Unix(),
	}

	// 3. 创建 Token，指定 HS256 签名算法
	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		claims,
	)

	// 4. 使用密钥签名，返回完整 JWT 字符串
	return token.SignedString(secret)
}

func main() {
	// 从环境变量读取 Base64 编码的密钥
	secretText := os.Getenv("JWT_SECRET")
	if secretText == "" {
		log.Fatal("JWT_SECRET is required")
	}

	// 解码为原始密钥字节
	secret, err := base64.StdEncoding.DecodeString(secretText)
	if err != nil || len(secret) < 32 {
		log.Fatal("JWT_SECRET must encode at least 32 random bytes")
	}

	token, err := generateToken(secret)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(token)
}
