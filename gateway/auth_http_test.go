package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginHandlerIssuesRefreshCookie(t *testing.T) {

	manager, ctx := newTestRefreshManager(t)

	users, err := NewMemoryUserStore(
		"admin",
		"test-password-123",
	)
	if err != nil {
		t.Fatal(err)
	}

	secret := []byte(strings.Repeat("s", 32))

	body := `{
        "username": "admin",
        "password": "test-password-123"
    }`

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader(body),
	)

	recorder := httptest.NewRecorder()

	handler := loginHandler(
		secret,
		users,
		manager,
		false,
	)

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	var response LoginResponse

	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	if response.AccessToken == "" {
		t.Fatal("access token is empty")
	}

	claims, err := verifyJWT(response.AccessToken, secret)
	if err != nil {
		t.Fatalf("verify access token failed: %v", err)
	}

	if claims.UserID != "dev-user-001" {
		t.Fatalf("unexpected user ID: %s", claims.UserID)
	}

	// 检查 Refresh Token Cookie
	result := recorder.Result()
	cookies := result.Cookies()

	var refreshCookie *http.Cookie

	for _, cookie := range cookies {
		if cookie.Name == "refresh_token" {
			refreshCookie = cookie
			break
		}
	}

	if refreshCookie == nil {
		t.Fatal("refresh token cookie not found")
	}

	if !refreshCookie.HttpOnly {
		t.Fatal("refresh token cookie must be HttpOnly")
	}

	if refreshCookie.Path != "/auth" {
		t.Fatalf("unexpected cookie path: %s", refreshCookie.Path)
	}

	// 验证 Cookie 对应的 Token 确实存储在 Redis
	rotated, err := manager.Rotate(
		ctx,
		refreshCookie.Value,
	)
	if err != nil {
		t.Fatalf("refresh token not stored correctly: %v", err)
	}

	if rotated.UserID != "dev-user-001" {
		t.Fatalf("unexpected user ID: %s", rotated.UserID)
	}
}

func TestRefreshHandler(t *testing.T) {

	manager, ctx := newTestRefreshManager(t)

	secret := []byte(strings.Repeat("s", 32))

	issued, err := manager.Issue(ctx, "dev-user-001")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/refresh",
		nil,
	)

	req.AddCookie(&http.Cookie{
		Name:  "refresh_token",
		Value: issued.Token,
	})

	recorder := httptest.NewRecorder()

	refreshHandler(
		secret,
		manager,
		false,
	).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s",
			recorder.Code,
			recorder.Body.String(),
		)
	}

	var response LoginResponse

	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	if response.AccessToken == "" {
		t.Fatal("new access token is empty")
	}

	if _, err := verifyJWT(response.AccessToken, secret); err != nil {
		t.Fatalf("verify new access token failed: %v", err)
	}

	// 检查新 Cookie
	var newCookie *http.Cookie

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "refresh_token" {
			newCookie = cookie
			break
		}
	}

	if newCookie == nil {
		t.Fatal("new refresh token cookie not found")
	}

	if newCookie.Value == issued.Token {
		t.Fatal("refresh token was not rotated")
	}

	// 旧 Token 再次使用应该被识别为重放
	_, err = manager.Rotate(ctx, issued.Token)

	if !errors.Is(err, errRefreshTokenReuse) {
		t.Fatalf("expected reuse error, got: %v", err)
	}
}

func TestLogoutHandler(t *testing.T) {

	manager, ctx := newTestRefreshManager(t)

	issued, err := manager.Issue(ctx, "dev-user-001")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/logout",
		nil,
	)

	req.AddCookie(&http.Cookie{
		Name:  "refresh_token",
		Value: issued.Token,
	})

	recorder := httptest.NewRecorder()

	logoutHandler(
		manager,
		false,
	).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", recorder.Code)
	}

	// 检查 Cookie 是否被清除
	var cleared bool

	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "refresh_token" && cookie.MaxAge == -1 {
			cleared = true
			break
		}
	}

	if !cleared {
		t.Fatal("refresh token cookie was not cleared")
	}

	// 验证 Redis 中的旧 Token 已经撤销
	_, err = manager.Rotate(ctx, issued.Token)

	if !errors.Is(err, errInvalidRefreshToken) {
		t.Fatalf("expected invalid token, got: %v", err)
	}
}
