package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const validConfig = `
server:
  addr: ":18080"
  read_header_timeout: 3s
  shutdown_timeout: 5s

auth:
  secret_file: data/test.secret
  users:
    - username: admin
      user_id: admin-001
      roles: [admin]
      password_env: TEST_ADMIN_PASSWORD

mesh:
  control_plane_url: http://127.0.0.1:18000

rate_limit:
  default:
    rps: 10
    burst: 20
  routes:
    "/api/orders/":
      rps: 5
      burst: 10

routes:
  - name: users
    match:
      path_prefix: /api/users/
      methods: [GET]
      strip_prefix: /api
    upstream:
      service: user-service
      timeout: 2s
    auth:
      required: true
      roles: [admin]
`

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("TEST_ADMIN_PASSWORD", "test-only-"+strconv.Itoa(os.Getpid()))

	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Addr != ":18080" {
		t.Fatalf("addr = %q", cfg.Server.Addr)
	}
	if cfg.Server.ReadHeaderTimeout != 3*time.Second {
		t.Fatalf("read_header_timeout = %v", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Auth.Issuer != "go-gateway-reference" || cfg.Auth.Audience != "go-gateway" {
		t.Fatalf("auth defaults missing: %+v", cfg.Auth)
	}
	if cfg.Mesh.Namespace != "default" || cfg.Mesh.Identity != "edge-gateway" {
		t.Fatalf("mesh defaults missing: %+v", cfg.Mesh)
	}
	if cfg.Routes[0].Upstream.Timeout != 2*time.Second {
		t.Fatalf("route timeout = %v", cfg.Routes[0].Upstream.Timeout)
	}
	if cfg.Routes[0].Upstream.LoadBalancer != "round_robin" {
		t.Fatalf("load balancer = %q", cfg.Routes[0].Upstream.LoadBalancer)
	}
}

func TestLoadRejectsMissingMeshURL(t *testing.T) {
	t.Setenv("TEST_ADMIN_PASSWORD", "test-only-"+strconv.Itoa(os.Getpid()))

	path := filepath.Join(t.TempDir(), "gateway.yaml")
	text := `
auth:
  users:
    - username: admin
      user_id: admin-001
      password_env: TEST_ADMIN_PASSWORD
routes:
  - name: users
    match:
      path_prefix: /api/users/
    upstream:
      service: user-service
`
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for missing control plane URL")
	}
}

func TestPasswordEnvTakesPrecedence(t *testing.T) {
	envPassword := "env-" + strconv.Itoa(os.Getpid())
	t.Setenv("TEST_USER_PASSWORD", envPassword)

	user := UserConfig{
		Username:    "admin",
		UserID:      "admin-001",
		PasswordEnv: "TEST_USER_PASSWORD",
		Password:    "file-" + strconv.Itoa(os.Getpid()),
	}

	password, err := user.ResolvePassword()
	if err != nil {
		t.Fatal(err)
	}
	if password != envPassword {
		t.Fatalf("password = %q, want environment value", password)
	}
}

func TestPlainPasswordFallback(t *testing.T) {
	plain := "plain-" + strconv.Itoa(os.Getpid())

	user := UserConfig{Username: "admin", UserID: "admin-001", Password: plain}

	password, err := user.ResolvePassword()
	if err != nil {
		t.Fatal(err)
	}
	if password != plain {
		t.Fatalf("password = %q, want plain value", password)
	}
}
