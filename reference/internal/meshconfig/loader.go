// Package meshconfig 加载并校验 VirtualService / DestinationRule 配置。
//
// 一个目录下可以有多个 *.yaml 文件，每个文件可以包含多个 YAML 文档
// （用 --- 分隔）。控制面按文件名排序加载，保证结果稳定。
package meshconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"example.com/go-gateway-reference/internal/meshapi"
)

// LoadDir 加载目录下的全部网格配置。
func LoadDir(dir string) ([]meshapi.VirtualService, []meshapi.DestinationRule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read mesh config dir %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".yaml" || ext == ".yml" {
			names = append(names, entry.Name())
		}
	}

	sort.Strings(names)

	var virtualServices []meshapi.VirtualService
	var destinationRules []meshapi.DestinationRule

	for _, name := range names {
		path := filepath.Join(dir, name)

		vs, dr, err := loadFile(path)
		if err != nil {
			return nil, nil, err
		}

		virtualServices = append(virtualServices, vs...)
		destinationRules = append(destinationRules, dr...)
	}

	normalize(virtualServices, destinationRules)

	if err := Validate(virtualServices, destinationRules); err != nil {
		return nil, nil, err
	}

	return virtualServices, destinationRules, nil
}

func loadFile(path string) ([]meshapi.VirtualService, []meshapi.DestinationRule, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)

	var virtualServices []meshapi.VirtualService
	var destinationRules []meshapi.DestinationRule

	for index := 0; ; index++ {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, fmt.Errorf("%s: document %d: %w", path, index+1, err)
		}

		if len(node.Content) == 0 {
			continue
		}

		var header struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
		}
		if err := node.Decode(&header); err != nil {
			return nil, nil, fmt.Errorf("%s: document %d: %w", path, index+1, err)
		}

		switch header.Kind {
		case "VirtualService":
			var vs meshapi.VirtualService
			if err := node.Decode(&vs); err != nil {
				return nil, nil, fmt.Errorf("%s: document %d: %w", path, index+1, err)
			}
			virtualServices = append(virtualServices, vs)
		case "DestinationRule":
			var dr meshapi.DestinationRule
			if err := node.Decode(&dr); err != nil {
				return nil, nil, fmt.Errorf("%s: document %d: %w", path, index+1, err)
			}
			destinationRules = append(destinationRules, dr)
		case "":
			return nil, nil, fmt.Errorf("%s: document %d: kind is required", path, index+1)
		default:
			return nil, nil, fmt.Errorf("%s: document %d: unsupported kind %q", path, index+1, header.Kind)
		}
	}

	return virtualServices, destinationRules, nil
}

func normalize(virtualServices []meshapi.VirtualService, destinationRules []meshapi.DestinationRule) {
	for i := range virtualServices {
		vs := &virtualServices[i]
		if vs.Metadata.Namespace == "" {
			vs.Metadata.Namespace = "default"
		}

		for j := range vs.Spec.HTTP {
			route := &vs.Spec.HTTP[j]

			for k := range route.Route {
				destination := &route.Route[k].Destination
				if destination.Host == "" && len(vs.Spec.Hosts) > 0 {
					destination.Host = vs.Spec.Hosts[0]
				}
				if route.Route[k].Weight <= 0 {
					route.Route[k].Weight = 1
				}
			}
		}
	}

	for i := range destinationRules {
		if destinationRules[i].Metadata.Namespace == "" {
			destinationRules[i].Metadata.Namespace = "default"
		}
	}
}

// Validate 校验配置语义。
func Validate(virtualServices []meshapi.VirtualService, destinationRules []meshapi.DestinationRule) error {
	ruleByHost := make(map[string]*meshapi.DestinationRule, len(destinationRules))
	subsetByHost := make(map[string]map[string]struct{}, len(destinationRules))

	for i := range destinationRules {
		dr := &destinationRules[i]

		if strings.TrimSpace(dr.Spec.Host) == "" {
			return fmt.Errorf("DestinationRule %q: spec.host is required", dr.Metadata.Name)
		}
		if _, exists := ruleByHost[dr.Spec.Host]; exists {
			return fmt.Errorf("duplicate DestinationRule for host %q", dr.Spec.Host)
		}
		ruleByHost[dr.Spec.Host] = dr

		subsets := make(map[string]struct{}, len(dr.Spec.Subsets))
		for _, subset := range dr.Spec.Subsets {
			if subset.Name == "" {
				return fmt.Errorf("DestinationRule %q: subset name is required", dr.Metadata.Name)
			}
			if _, exists := subsets[subset.Name]; exists {
				return fmt.Errorf("DestinationRule %q: duplicate subset %q", dr.Metadata.Name, subset.Name)
			}
			if len(subset.Labels) == 0 {
				return fmt.Errorf("DestinationRule %q: subset %q labels cannot be empty", dr.Metadata.Name, subset.Name)
			}
			subsets[subset.Name] = struct{}{}
		}
		subsetByHost[dr.Spec.Host] = subsets

		switch strings.ToLower(dr.Spec.TrafficPolicy.LoadBalancer) {
		case "", "round_robin", "random", "least_request":
		default:
			return fmt.Errorf("DestinationRule %q: unknown load_balancer %q", dr.Metadata.Name, dr.Spec.TrafficPolicy.LoadBalancer)
		}

		switch strings.ToUpper(dr.Spec.TrafficPolicy.PeerAuthentication) {
		case "", "STRICT", "PERMISSIVE":
		default:
			return fmt.Errorf("DestinationRule %q: unknown peer_authentication %q", dr.Metadata.Name, dr.Spec.TrafficPolicy.PeerAuthentication)
		}

		if dr.Spec.TrafficPolicy.ConnectionPool.MaxConnections < 0 {
			return fmt.Errorf("DestinationRule %q: max_connections cannot be negative", dr.Metadata.Name)
		}
		if dr.Spec.TrafficPolicy.ConnectionPool.MaxPendingRequests < 0 {
			return fmt.Errorf("DestinationRule %q: max_pending_requests cannot be negative", dr.Metadata.Name)
		}
		if dr.Spec.TrafficPolicy.OutlierDetection.Consecutive5xx < 0 {
			return fmt.Errorf("DestinationRule %q: consecutive_5xx cannot be negative", dr.Metadata.Name)
		}
		if dr.Spec.TrafficPolicy.OutlierDetection.EjectionTime < 0 {
			return fmt.Errorf("DestinationRule %q: ejection_time cannot be negative", dr.Metadata.Name)
		}
	}

	names := make(map[string]struct{}, len(virtualServices))

	for _, vs := range virtualServices {
		key := vs.Metadata.Namespace + "/" + vs.Metadata.Name
		if vs.Metadata.Name == "" {
			return fmt.Errorf("VirtualService: metadata.name is required")
		}
		if _, exists := names[key]; exists {
			return fmt.Errorf("duplicate VirtualService %q", key)
		}
		names[key] = struct{}{}

		if len(vs.Spec.Hosts) == 0 {
			return fmt.Errorf("VirtualService %q: spec.hosts cannot be empty", vs.Metadata.Name)
		}
		for _, host := range vs.Spec.Hosts {
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("VirtualService %q: host cannot be empty", vs.Metadata.Name)
			}
		}

		if len(vs.Spec.HTTP) == 0 {
			return fmt.Errorf("VirtualService %q: spec.http cannot be empty", vs.Metadata.Name)
		}

		for _, route := range vs.Spec.HTTP {
			if len(route.Route) == 0 {
				return fmt.Errorf("VirtualService %q: http.route cannot be empty", vs.Metadata.Name)
			}

			weightSum := 0
			for _, destinationWeight := range route.Route {
				if destinationWeight.Weight < 0 {
					return fmt.Errorf("VirtualService %q: route weight cannot be negative", vs.Metadata.Name)
				}
				weightSum += destinationWeight.Weight

				destination := destinationWeight.Destination
				if destination.Host == "" {
					return fmt.Errorf("VirtualService %q: destination host is required", vs.Metadata.Name)
				}
				if destination.Subset != "" {
					subsets, ok := subsetByHost[destination.Host]
					if !ok {
						return fmt.Errorf("VirtualService %q: subset %q requires a DestinationRule for host %q",
							vs.Metadata.Name, destination.Subset, destination.Host)
					}
					if _, ok := subsets[destination.Subset]; !ok {
						return fmt.Errorf("VirtualService %q: unknown subset %q for host %q",
							vs.Metadata.Name, destination.Subset, destination.Host)
					}
				}
			}

			if weightSum <= 0 {
				return fmt.Errorf("VirtualService %q: route weight sum must be positive", vs.Metadata.Name)
			}

			if route.Timeout < 0 {
				return fmt.Errorf("VirtualService %q: timeout cannot be negative", vs.Metadata.Name)
			}

			if route.Retries != nil {
				if route.Retries.Attempts < 0 || route.Retries.Attempts > 10 {
					return fmt.Errorf("VirtualService %q: retries.attempts must be between 0 and 10", vs.Metadata.Name)
				}
				if route.Retries.Attempts > 0 && route.Retries.PerTryTimeout <= 0 {
					return fmt.Errorf("VirtualService %q: retries.per_try_timeout must be positive", vs.Metadata.Name)
				}
			}

			if route.Fault != nil {
				if route.Fault.Abort != nil {
					if route.Fault.Abort.Percentage < 0 || route.Fault.Abort.Percentage > 100 {
						return fmt.Errorf("VirtualService %q: abort percentage must be between 0 and 100", vs.Metadata.Name)
					}
					if route.Fault.Abort.HTTPStatus < 100 || route.Fault.Abort.HTTPStatus > 599 {
						return fmt.Errorf("VirtualService %q: abort http_status must be between 100 and 599", vs.Metadata.Name)
					}
				}
				if route.Fault.Delay != nil {
					if route.Fault.Delay.Percentage < 0 || route.Fault.Delay.Percentage > 100 {
						return fmt.Errorf("VirtualService %q: delay percentage must be between 0 and 100", vs.Metadata.Name)
					}
					if route.Fault.Delay.FixedDelay <= 0 {
						return fmt.Errorf("VirtualService %q: delay fixed_delay must be positive", vs.Metadata.Name)
					}
				}
			}
		}
	}

	return nil
}

// HashDir 计算目录下配置文件的稳定哈希，用于热重载检测。
func HashDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".yaml" || ext == ".yml" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	hash := sha256.New()

	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}

		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write(data)
		hash.Write([]byte{0})
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
