
package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
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
}

// 用户仓库
type MemoryUserStore struct {
	username     string
	userID       string
	passwordHash []byte
}

// 创建测试用户
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

	return &MemoryUserStore{
		username:     username,
		userID:       "dev-user-001",
		passwordHash: passwordHash,
	}, nil
}

// 验证用户
func (s *MemoryUserStore) Authenticate(
	username string,
	password string,
) (string, error) {

	if strings.TrimSpace(username) != s.username {
		return "", errors.New("invalid credentials")
	}

	err := bcrypt.CompareHashAndPassword(
		s.passwordHash,
		[]byte(password),
	)
	if err != nil {
		return "", errors.New("invalid credentials")
	}

	return s.userID, nil
}

// 登录接口
func loginHandler(
	secret []byte,
	users *MemoryUserStore,
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

		userID, err := users.Authenticate(
			req.Username,
			req.Password,
		)
		if err != nil {
			http.Error(w, "Invalid username or password", http.StatusUnauthorized)
			return
		}

		accessToken, err := generateJWT(
			userID,
			secret,
			accessTokenTTL,
		)
		if err != nil {
			log.Printf("Generate JWT failed: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		response := LoginResponse{
			AccessToken: accessToken,
			TokenType:   "Bearer",
			ExpiresIn:   int64(accessTokenTTL.Seconds()),
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Printf("Encode response failed: %v", err)
		}
	}
}
