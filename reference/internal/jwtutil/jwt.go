// Package jwtutil 实现访问令牌、刷新令牌、轮换与吊销。
//
// access token 是 HS256 JWT，refresh token 是不透明随机串。刷新时旧
// refresh 立即失效（rotation），登出时 refresh 被删除、access 的 jti
// 进入 denylist，直到它自然过期。
package jwtutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims 是访问令牌中的业务声明。
type Claims struct {
	UserID string   `json:"user_id"`
	Roles  []string `json:"roles,omitempty"`
	jwt.RegisteredClaims
}

// TokenPair 是一次签发的结果。
type TokenPair struct {
	UserID           string    `json:"user_id"`
	Roles            []string  `json:"roles"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

type refreshSession struct {
	userID    string
	roles     []string
	expiresAt time.Time
}

// Manager 负责签发与校验令牌。
type Manager struct {
	secret     []byte
	issuer     string
	audience   string
	accessTTL  time.Duration
	refreshTTL time.Duration

	mu      sync.Mutex
	refresh map[string]*refreshSession
	revoked map[string]time.Time
}

// NewManager 创建令牌管理器。
func NewManager(secret []byte, issuer, audience string, accessTTL, refreshTTL time.Duration) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT secret must contain at least 32 bytes")
	}
	if issuer == "" {
		return nil, errors.New("JWT issuer cannot be empty")
	}
	if audience == "" {
		return nil, errors.New("JWT audience cannot be empty")
	}
	if accessTTL <= 0 {
		accessTTL = 5 * time.Minute
	}
	if refreshTTL <= 0 {
		refreshTTL = 30 * time.Minute
	}

	return &Manager{
		secret:     secret,
		issuer:     issuer,
		audience:   audience,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		refresh:    make(map[string]*refreshSession),
		revoked:    make(map[string]time.Time),
	}, nil
}

// AccessTTL 返回访问令牌有效期。
func (m *Manager) AccessTTL() time.Duration {
	return m.accessTTL
}

// RefreshTTL 返回刷新令牌有效期。
func (m *Manager) RefreshTTL() time.Duration {
	return m.refreshTTL
}

// Issue 为指定用户签发一对新令牌。
func (m *Manager) Issue(userID string, roles []string) (*TokenPair, error) {
	if userID == "" {
		return nil, errors.New("user ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.issueLocked(userID, roles)
}

// Refresh 用 refresh token 换取新令牌，并立即让旧 refresh 失效。
func (m *Manager) Refresh(refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, errors.New("refresh token is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.refresh[refreshToken]
	if !ok {
		return nil, errors.New("invalid refresh token")
	}

	delete(m.refresh, refreshToken)

	if time.Now().After(session.expiresAt) {
		return nil, errors.New("refresh token expired")
	}

	return m.issueLocked(session.userID, session.roles)
}

// RevokeRefresh 删除一个 refresh token；返回是否确实存在。
func (m *Manager) RevokeRefresh(refreshToken string) bool {
	if refreshToken == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.refresh[refreshToken]
	delete(m.refresh, refreshToken)
	return ok
}

// RevokeAccess 把访问令牌的 jti 放入 denylist。
func (m *Manager) RevokeAccess(jti string, expiresAt time.Time) {
	if jti == "" || expiresAt.Before(time.Now()) {
		return
	}

	m.mu.Lock()
	m.revoked[jti] = expiresAt
	m.mu.Unlock()
}

// Verify 解析并校验访问令牌。
func (m *Manager) Verify(tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (any, error) {
			return m.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithAudience(m.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		return nil, err
	}

	if !token.Valid || claims.UserID == "" || claims.Subject != claims.UserID || claims.ID == "" {
		return nil, errors.New("invalid JWT identity")
	}

	m.mu.Lock()
	revokedUntil, revoked := m.revoked[claims.ID]
	m.mu.Unlock()

	if revoked && time.Now().Before(revokedUntil) {
		return nil, errors.New("token has been revoked")
	}

	return claims, nil
}

// Cleanup 清理过期的 refresh 与 denylist 记录。
func (m *Manager) Cleanup() {
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	for token, session := range m.refresh {
		if now.After(session.expiresAt) {
			delete(m.refresh, token)
		}
	}

	for jti, expiresAt := range m.revoked {
		if now.After(expiresAt) {
			delete(m.revoked, jti)
		}
	}
}

func (m *Manager) issueLocked(userID string, roles []string) (*TokenPair, error) {
	now := time.Now()
	accessExpiry := now.Add(m.accessTTL)
	refreshExpiry := now.Add(m.refreshTTL)

	jti, err := randomHex(16)
	if err != nil {
		return nil, err
	}

	refreshToken, err := randomHex(32)
	if err != nil {
		return nil, err
	}

	claims := &Claims{
		UserID: userID,
		Roles:  append([]string(nil), roles...),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{m.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExpiry),
			ID:        jti,
		},
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return nil, err
	}

	m.refresh[refreshToken] = &refreshSession{
		userID:    userID,
		roles:     append([]string(nil), roles...),
		expiresAt: refreshExpiry,
	}

	return &TokenPair{
		UserID:           userID,
		Roles:            append([]string(nil), roles...),
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpiry,
		RefreshExpiresAt: refreshExpiry,
	}, nil
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf), nil
}
