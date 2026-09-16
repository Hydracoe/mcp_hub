package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/hydra/mcp-hub/internal/downstream"
)

// buildSpec 组装探活配置（add / probe 共用）。transport 没写就按参数猜：
// 有 --command 是 stdio，有 --url 是 HTTP。
func buildSpec(
	rawURL, transport, command string,
	args []string,
	headers headerList,
	timeout time.Duration,
	legacyOnly bool,
	pinnedProto string,
) (downstream.Spec, error) {
	tr := downstream.Transport(transport)
	if tr == "" {
		switch {
		case command != "":
			tr = downstream.TransportStdio
		case rawURL != "":
			tr = downstream.TransportStreamableHTTP
		default:
			return downstream.Spec{}, fmt.Errorf("必须提供 --url 或 --command")
		}
	}

	spec := downstream.Spec{
		Key:             deriveKey(rawURL, command),
		Name:            deriveKey(rawURL, command),
		Transport:       tr,
		URL:             rawURL,
		Headers:         headers,
		Command:         command,
		Args:            args,
		Timeout:         timeout,
		LegacyOnly:      legacyOnly,
		ProtocolVersion: pinnedProto,
	}
	if err := spec.Validate(); err != nil {
		return downstream.Spec{}, err
	}
	return spec, nil
}

// deriveKey 从 URL 域名或命令名猜一个显示用的前缀。
func deriveKey(rawURL, command string) string {
	if command != "" {
		base := command
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		base = strings.TrimSuffix(base, ".exe")
		if base != "" {
			return sanitizeKey(base)
		}
	}
	if rawURL != "" {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
		host := trimmed
		if i := strings.IndexAny(host, "/:"); i >= 0 {
			host = host[:i]
		}
		if host != "" {
			return sanitizeKey(host)
		}
	}
	return "server"
}

func sanitizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// headerList 让 --header 可以重复写。
type headerList map[string]string

func (h *headerList) String() string { return "" }

func (h *headerList) Set(v string) error {
	i := strings.Index(v, ":")
	if i < 0 {
		return fmt.Errorf("header 需要 \"Key: Value\" 格式，收到 %q", v)
	}
	if *h == nil {
		*h = map[string]string{}
	}
	(*h)[strings.TrimSpace(v[:i])] = strings.TrimSpace(v[i+1:])
	return nil
}

func (h *headerList) Type() string { return "string" }
