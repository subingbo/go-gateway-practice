package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRBACUserStoreRoles(t *testing.T) {

	store, err := NewMemoryUserStore(
		"admin",
		"admin-password",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.AddRegularUser(
		"reader",
		"reader-password",
	); err != nil {
		t.Fatal(err)
	}

	admin, err := store.AuthenticateUser(
		"admin",
		"admin-password",
	)
	if err != nil {
		t.Fatal(err)
	}

	if admin.Role != RoleAdmin {
		t.Fatalf("expected admin, got %s", admin.Role)
	}

	reader, err := store.AuthenticateUser(
		"reader",
		"reader-password",
	)
	if err != nil {
		t.Fatal(err)
	}

	if reader.Role != RoleUser {
		t.Fatalf("expected user, got %s", reader.Role)
	}
}

func TestRBACAdminOnlyMiddleware(t *testing.T) {

	next := http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := adminOnlyMiddleware(next)

	tests := []struct {
		name       string
		role       string
		wantStatus int
	}{
		{
			name:       "admin allowed",
			role:       "admin",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "user forbidden",
			role:       "user",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "missing role forbidden",
			role:       "",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {

			req := httptest.NewRequest(
				http.MethodPost,
				"/api/upstreams",
				nil,
			)

			if tt.role != "" {
				ctx := context.WithValue(
					req.Context(),
					userRoleContextKey{},
					tt.role,
				)

				req = req.WithContext(ctx)
			}

			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf(
					"expected status %d, got %d",
					tt.wantStatus,
					recorder.Code,
				)
			}
		})
	}
}
