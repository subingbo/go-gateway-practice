// Package route 实现边缘网关的路由匹配。
//
// 规则按“最长路径前缀优先”选择；同一前缀下按配置顺序取第一条。
// method 与 headers 是可选的附加条件，空表示不限制。
package route

import (
	"net"
	"net/http"
	"strings"
)

// Match 描述一条路由的匹配条件。
type Match struct {
	Host       string
	PathPrefix string
	Methods    []string
	Headers    map[string]string
}

// Matches 判断请求是否满足全部条件。
func (m Match) Matches(r *http.Request) bool {
	if m.Host != "" && !strings.EqualFold(m.Host, requestHost(r)) {
		return false
	}

	if m.PathPrefix != "" && !strings.HasPrefix(r.URL.Path, m.PathPrefix) {
		return false
	}

	if len(m.Methods) > 0 {
		matched := false
		for _, method := range m.Methods {
			if strings.EqualFold(method, r.Method) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for key, want := range m.Headers {
		if r.Header.Get(key) != want {
			return false
		}
	}

	return true
}

// Rule 把匹配条件和业务对象绑在一起；Value 由调用方解释。
type Rule struct {
	Match Match
	Value any
}

// Best 返回匹配规则的下标，优先选择最长 PathPrefix。
func Best(rules []Rule, r *http.Request) (int, bool) {
	best := -1
	bestLen := -1

	for i, rule := range rules {
		if !rule.Match.Matches(r) {
			continue
		}

		if l := len(rule.Match.PathPrefix); l > bestLen {
			best = i
			bestLen = l
		}
	}

	return best, best >= 0
}

// requestHost 去掉 Host 里的端口，便于和配置中的域名比较。
func requestHost(r *http.Request) string {
	host := r.Host

	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}

	return host
}
