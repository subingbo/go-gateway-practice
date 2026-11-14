package meshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validMeshConfig = `
apiVersion: mesh/v1
kind: VirtualService
metadata:
  name: user-service
spec:
  hosts: [user-service]
  http:
    - name: default
      route:
        - destination:
            subset: v1
          weight: 90
        - destination:
            subset: v2
          weight: 10
      timeout: 2s
      retries:
        attempts: 2
        per_try_timeout: 500ms
        retry_on: [5xx, reset]
---
apiVersion: mesh/v1
kind: DestinationRule
metadata:
  name: user-service
spec:
  host: user-service
  subsets:
    - name: v1
      labels: { version: v1 }
    - name: v2
      labels: { version: v2 }
  traffic_policy:
    load_balancer: least_request
    outlier_detection:
      consecutive_5xx: 5
      ejection_time: 10s
    peer_authentication: PERMISSIVE
`

func TestLoadDirParsesDurationsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mesh.yaml"), []byte(validMeshConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	virtualServices, destinationRules, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(virtualServices) != 1 || len(destinationRules) != 1 {
		t.Fatalf("unexpected counts: %d vs, %d dr", len(virtualServices), len(destinationRules))
	}

	route := virtualServices[0].Spec.HTTP[0]
	if route.Timeout != 2*time.Second {
		t.Fatalf("timeout = %v", route.Timeout)
	}
	if route.Retries == nil || route.Retries.PerTryTimeout != 500*time.Millisecond {
		t.Fatalf("retry duration not parsed: %+v", route.Retries)
	}
	if route.Route[0].Destination.Host != "user-service" {
		t.Fatalf("destination host default = %q", route.Route[0].Destination.Host)
	}

	policy := destinationRules[0].Spec.TrafficPolicy
	if policy.OutlierDetection.EjectionTime != 10*time.Second {
		t.Fatalf("ejection time = %v", policy.OutlierDetection.EjectionTime)
	}
}

func TestLoadDirRejectsUnknownSubset(t *testing.T) {
	dir := t.TempDir()

	text := strings.ReplaceAll(validMeshConfig, "subset: v2", "subset: canary")
	if err := os.WriteFile(filepath.Join(dir, "mesh.yaml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadDir(dir); err == nil {
		t.Fatal("expected unknown subset error")
	}
}
