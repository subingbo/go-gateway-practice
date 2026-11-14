package jwtutil

import (
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()

	secret := []byte("0123456789abcdef0123456789abcdef")
	manager, err := NewManager(secret, "test-issuer", "test-audience", time.Minute, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestIssueVerifyAndRevoke(t *testing.T) {
	manager := testManager(t)

	pair, err := manager.Issue("user-1", []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}

	claims, err := manager.Verify(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "user-1" || len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	manager.RevokeAccess(claims.ID, claims.ExpiresAt.Time)
	if _, err := manager.Verify(pair.AccessToken); err == nil {
		t.Fatal("revoked token should fail verification")
	}
}

func TestRefreshRotatesToken(t *testing.T) {
	manager := testManager(t)

	pair, err := manager.Issue("user-1", nil)
	if err != nil {
		t.Fatal(err)
	}

	next, err := manager.Refresh(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.RefreshToken == pair.RefreshToken {
		t.Fatal("refresh token should rotate")
	}

	if _, err := manager.Refresh(pair.RefreshToken); err == nil {
		t.Fatal("old refresh token should be invalid after rotation")
	}
}

func TestExpiredAccessTokenFails(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	manager, err := NewManager(secret, "test-issuer", "test-audience", 20*time.Millisecond, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	pair, err := manager.Issue("user-1", nil)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(40 * time.Millisecond)

	if _, err := manager.Verify(pair.AccessToken); err == nil {
		t.Fatal("expired token should fail verification")
	}
}
