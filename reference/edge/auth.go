package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"example.com/go-gateway-reference/internal/config"
	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/internal/jwtutil"
)

var errMissingToken = errors.New("missing bearer token")

type userAccount struct {
	userID       string
	roles        []string
	passwordHash []byte
}

type authService struct {
	manager *jwtutil.Manager

	mu    sync.RWMutex
	users map[string]*userAccount
}

func newAuthService(cfg config.AuthConfig, secret []byte) (*authService, error) {
	manager, err := jwtutil.NewManager(secret, cfg.Issuer, cfg.Audience, cfg.AccessTTL, cfg.RefreshTTL)
	if err != nil {
		return nil, err
	}

	service := &authService{
		manager: manager,
		users:   make(map[string]*userAccount),
	}

	if err := service.SetUsers(cfg.Users); err != nil {
		return nil, err
	}

	return service, nil
}

// SetUsers 重新加载用户；配置热更新时调用。
func (a *authService) SetUsers(users []config.UserConfig) error {
	next := make(map[string]*userAccount, len(users))

	for _, user := range users {
		password, err := user.ResolvePassword()
		if err != nil {
			return err
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}

		next[user.Username] = &userAccount{
			userID:       user.UserID,
			roles:        append([]string(nil), user.Roles...),
			passwordHash: hash,
		}
	}

	a.mu.Lock()
	a.users = next
	a.mu.Unlock()

	return nil
}

func (a *authService) Authenticate(username, password string) (*userAccount, error) {
	a.mu.RLock()
	user, ok := a.users[username]
	a.mu.RUnlock()

	if !ok {
		return nil, errors.New("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword(user.passwordHash, []byte(password)); err != nil {
		return nil, errors.New("invalid credentials")
	}

	return user, nil
}

func (a *authService) Verify(token string) (*jwtutil.Claims, error) {
	return a.manager.Verify(token)
}

type claimsContextKey struct{}

func withClaims(ctx context.Context, claims *jwtutil.Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

func claimsFromContext(ctx context.Context) *jwtutil.Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*jwtutil.Claims)
	return claims
}

type tokenResponse struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	TokenType        string    `json:"token_type"`
	ExpiresIn        int64     `json:"expires_in"`
	RefreshExpiresIn int64     `json:"refresh_expires_in"`
	UserID           string    `json:"user_id"`
	Roles            []string  `json:"roles"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

func tokenResponseFrom(pair *jwtutil.TokenPair) tokenResponse {
	return tokenResponse{
		AccessToken:      pair.AccessToken,
		RefreshToken:     pair.RefreshToken,
		TokenType:        "Bearer",
		ExpiresIn:        int64(time.Until(pair.AccessExpiresAt).Seconds()),
		RefreshExpiresIn: int64(time.Until(pair.RefreshExpiresAt).Seconds()),
		UserID:           pair.UserID,
		Roles:            pair.Roles,
		AccessExpiresAt:  pair.AccessExpiresAt,
		RefreshExpiresAt: pair.RefreshExpiresAt,
	}
}

func (g *Gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := decodeAuthJSON(w, r, &request); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	request.Username = strings.TrimSpace(request.Username)
	if request.Username == "" || request.Password == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "username and password are required")
		return
	}

	user, err := g.auth.Authenticate(request.Username, request.Password)
	if err != nil {
		g.authFailures.Inc("invalid_credentials")
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	pair, err := g.auth.manager.Issue(user.userID, user.roles)
	if err != nil {
		g.logger.Error("issue token failed", "error", err)
		httpx.ErrorJSON(w, http.StatusInternalServerError, "issue token failed")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, tokenResponseFrom(pair))
}

func (g *Gateway) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refresh_token"`
	}

	if err := decodeAuthJSON(w, r, &request); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	pair, err := g.auth.manager.Refresh(request.RefreshToken)
	if err != nil {
		g.authFailures.Inc("invalid_refresh")
		httpx.ErrorJSON(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, tokenResponseFrom(pair))
}

func (g *Gateway) handleLogout(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refresh_token"`
	}

	// 允许空 body：只带 access token 也能登出。
	_ = decodeAuthJSON(w, r, &request)

	if request.RefreshToken != "" {
		g.auth.manager.RevokeRefresh(request.RefreshToken)
	}

	if claims := g.claimsFromRequest(r); claims != nil {
		g.auth.manager.RevokeAccess(claims.ID, claims.ExpiresAt.Time)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) claimsFromRequest(r *http.Request) *jwtutil.Claims {
	token := bearerToken(r)
	if token == "" {
		return nil
	}

	claims, err := g.auth.Verify(token)
	if err != nil {
		return nil
	}

	return claims
}

func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request, policy config.AuthPolicy) (*jwtutil.Claims, bool) {
	claims, err := g.auth.authenticateRequest(r)
	if err != nil {
		reason := "invalid_token"
		if errors.Is(err, errMissingToken) {
			reason = "missing_token"
		}

		g.authFailures.Inc(reason)
		httpx.ErrorJSON(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}

	if len(policy.Roles) > 0 && !hasAnyRole(claims.Roles, policy.Roles) {
		g.authFailures.Inc("forbidden")
		httpx.ErrorJSON(w, http.StatusForbidden, "forbidden")
		return nil, false
	}

	return claims, true
}

func (a *authService) authenticateRequest(r *http.Request) (*jwtutil.Claims, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, errMissingToken
	}

	return a.Verify(token)
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func hasAnyRole(actual, required []string) bool {
	for _, want := range required {
		for _, have := range actual {
			if strings.EqualFold(want, have) {
				return true
			}
		}
	}
	return false
}

func decodeAuthJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	return decoder.Decode(dst)
}
