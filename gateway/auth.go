package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const accessTokenTTL = 15 * time.Minute

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Role        string `json:"role"`
}

type UserRole string

const (
	RoleAdmin UserRole = "admin"
	RoleUser  UserRole = "user"
)

// 用户身份
type UserIdentity struct {
	ID   string
	Role UserRole
}

// 内部用户记录
type storedUser struct {
	username     string
	userID       string
	passwordHash []byte
	role         UserRole
}

// 用户仓库
type MemoryUserStore struct {
	mu          sync.RWMutex
	usersByName map[string]storedUser
	usersByID   map[string]storedUser
}

// 创建用户仓库，默认创建一个管理员
func NewMemoryUserStore(
	username string,
	password string,
) (*MemoryUserStore, error) {

	username = strings.TrimSpace(username)

	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}

	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte(password),
		bcrypt.DefaultCost,
	)
	if err != nil {
		return nil, err
	}

	store := &MemoryUserStore{
		usersByName: make(map[string]storedUser),
		usersByID:   make(map[string]storedUser),
	}

	admin := storedUser{
		username:     username,
		userID:       "dev-user-001",
		passwordHash: passwordHash,
		role:         RoleAdmin,
	}

	store.usersByName[username] = admin
	store.usersByID[admin.userID] = admin

	return store, nil
}

// 添加普通用户
func (s *MemoryUserStore) AddRegularUser(
	username string,
	password string,
) error {

	if s == nil {
		return errors.New("user store is nil")
	}

	username = strings.TrimSpace(username)

	if username == "" || password == "" {
		return errors.New("username and password are required")
	}

	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte(password),
		bcrypt.DefaultCost,
	)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.usersByName[username]; exists {
		return errors.New("username already exists")
	}

	// 生成不重复的用户 ID
	var userID string

	for i := len(s.usersByID) + 1; ; i++ {
		candidate := fmt.Sprintf("dev-user-%03d", i)

		if _, exists := s.usersByID[candidate]; !exists {
			userID = candidate
			break
		}
	}

	user := storedUser{
		username:     username,
		userID:       userID,
		passwordHash: passwordHash,
		role:         RoleUser,
	}

	s.usersByName[username] = user
	s.usersByID[userID] = user

	return nil
}

// 验证用户并返回身份信息
func (s *MemoryUserStore) AuthenticateUser(
	username string,
	password string,
) (UserIdentity, error) {

	if s == nil {
		return UserIdentity{}, errors.New("invalid credentials")
	}

	s.mu.RLock()
	user, ok := s.usersByName[strings.TrimSpace(username)]
	s.mu.RUnlock()

	if !ok {
		return UserIdentity{}, errors.New("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword(
		user.passwordHash,
		[]byte(password),
	); err != nil {
		return UserIdentity{}, errors.New("invalid credentials")
	}

	return UserIdentity{
		ID:   user.userID,
		Role: user.role,
	}, nil
}

// 保留原来的 Authenticate 方法，兼容已有代码
func (s *MemoryUserStore) Authenticate(
	username string,
	password string,
) (string, error) {

	identity, err := s.AuthenticateUser(username, password)
	if err != nil {
		return "", err
	}

	return identity.ID, nil
}

// 根据用户 ID 查询角色
func (s *MemoryUserStore) RoleForUserID(
	userID string,
) (UserRole, bool) {

	if s == nil {
		return "", false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.usersByID[userID]
	if !ok {
		return "", false
	}

	return user.role, true
}

// 登录接口
func loginHandler(
	secret []byte,
	users *MemoryUserStore,
	refreshManager *RefreshTokenManager,
	cookieSecure bool,
) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

		var req LoginRequest

		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(req.Username) == "" ||
			req.Password == "" {
			http.Error(w, "Missing credentials", http.StatusBadRequest)
			return
		}

		identity, err := users.AuthenticateUser(
			req.Username,
			req.Password,
		)
		if err != nil {
			http.Error(w, "Invalid username or password", http.StatusUnauthorized)
			return
		}

		// 1. 生成 Access Token
		accessToken, err := generateJWT(
			identity.ID,
			secret,
			accessTokenTTL,
			string(identity.Role),
		)
		if err != nil {
			log.Printf("Generate JWT failed: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// 2. 生成并保存 Refresh Token
		refreshGrant, err := refreshManager.Issue(
			r.Context(),
			identity.ID,
		)
		if err != nil {
			log.Printf("Issue refresh token failed: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// 3. 将 Refresh Token 放入 HttpOnly Cookie
		setRefreshTokenCookie(
			w,
			refreshGrant.Token,
			time.Duration(refreshGrant.ExpiresIn)*time.Second,
			cookieSecure,
		)

		// 4. 返回 Access Token
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		response := LoginResponse{
			AccessToken: accessToken,
			TokenType:   "Bearer",
			ExpiresIn:   int64(accessTokenTTL.Seconds()),
			Role:        string(identity.Role),
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Encode response failed: %v", err)
		}
	}
}

// 设置 Refresh Token Cookie
func setRefreshTokenCookie(
	w http.ResponseWriter,
	token string,
	ttl time.Duration,
	secure bool,
) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    token,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().Add(ttl),
	})
}

// 清除 Refresh Token Cookie
func clearRefreshTokenCookie(
	w http.ResponseWriter,
	secure bool,
) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/auth",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

// Refresh Token 续签接口
func refreshHandler(
	secret []byte,
	manager *RefreshTokenManager,
	cookieSecure bool,
	userStores ...*MemoryUserStore,
) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("Cache-Control", "no-store")

		// 1. 获取 Refresh Token Cookie
		cookie, err := r.Cookie("refresh_token")
		if err != nil || cookie.Value == "" {
			clearRefreshTokenCookie(w, cookieSecure)
			http.Error(w, "Refresh token required", http.StatusUnauthorized)
			return
		}

		// 2. 使用旧 Refresh Token 换取新 Token
		rotated, err := manager.Rotate(
			r.Context(),
			cookie.Value,
		)
		if err != nil {

			clearRefreshTokenCookie(w, cookieSecure)

			if errors.Is(err, errInvalidRefreshToken) ||
				errors.Is(err, errRefreshTokenReuse) {

				if errors.Is(err, errRefreshTokenReuse) {
					log.Printf("Refresh token reuse detected")
				}

				http.Error(w, "Invalid refresh token", http.StatusUnauthorized)
				return
			}

			log.Printf("Rotate refresh token failed: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// 根据用户 ID 重新获取角色
		role := RoleUser

		if len(userStores) > 0 && userStores[0] != nil {

			storedRole, ok := userStores[0].RoleForUserID(
				rotated.UserID,
			)

			if !ok {
				// 用户不存在，撤销刚刚轮换出来的 Refresh Token
				if _, revokeErr := manager.Revoke(
					r.Context(),
					rotated.Token,
				); revokeErr != nil {
					log.Printf("Revoke unknown user's token failed: %v", revokeErr)
				}

				clearRefreshTokenCookie(w, cookieSecure)

				http.Error(
					w,
					"Invalid user",
					http.StatusUnauthorized,
				)
				return
			}

			role = storedRole
		}

		// 3. 根据 Redis 返回的 UserID 签发新的 Access Token
		accessToken, err := generateJWT(
			rotated.UserID,
			secret,
			accessTokenTTL,
			string(role),
		)
		if err != nil {

			// 如果 Access Token 签发失败，撤销刚刚生成的新 Refresh Token
			if _, revokeErr := manager.Revoke(
				r.Context(),
				rotated.Token,
			); revokeErr != nil {
				log.Printf("Revoke new refresh token failed: %v", revokeErr)
			}

			clearRefreshTokenCookie(w, cookieSecure)

			log.Printf("Generate JWT failed: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// 4. 设置新的 Refresh Token Cookie
		setRefreshTokenCookie(
			w,
			rotated.Token,
			time.Duration(rotated.ExpiresIn)*time.Second,
			cookieSecure,
		)

		// 5. 返回新的 Access Token
		w.Header().Set("Content-Type", "application/json")

		response := LoginResponse{
			AccessToken: accessToken,
			TokenType:   "Bearer",
			ExpiresIn:   int64(accessTokenTTL.Seconds()),
			Role:        string(role),
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Encode refresh response failed: %v", err)
		}
	}
}

// 退出登录接口
func logoutHandler(
	manager *RefreshTokenManager,
	cookieSecure bool,
) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("Cache-Control", "no-store")

		// 1. 获取 Refresh Token
		cookie, err := r.Cookie("refresh_token")

		// 2. 如果 Cookie 存在，撤销 Redis 中的 Token
		if err == nil && cookie.Value != "" {

			_, revokeErr := manager.Revoke(
				r.Context(),
				cookie.Value,
			)

			if revokeErr != nil &&
				!errors.Is(revokeErr, errInvalidRefreshToken) {

				log.Printf("Revoke refresh token failed: %v", revokeErr)

				clearRefreshTokenCookie(w, cookieSecure)

				http.Error(
					w,
					"Logout temporarily unavailable",
					http.StatusServiceUnavailable,
				)
				return
			}
		}

		// 3. 清除浏览器 Cookie
		clearRefreshTokenCookie(w, cookieSecure)

		// 4. 返回 204 No Content
		w.WriteHeader(http.StatusNoContent)
	}
}
