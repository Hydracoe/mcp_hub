package downstream

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"time"
)

// probeNetwork 执行 L1 可达性探测：DNS → TCP → TLS。
// 刻意不发 HTTP 请求，只确认"能不能连上"，失败类型保持结构化（DNS/TLS）；
// HTTP 语义（401/4xx/5xx）需要真的发请求，由 Recorder 在 L2/L3 捕获。
func probeNetwork(ctx context.Context, rawURL string, timeout time.Duration) (time.Duration, ErrorKind, error) {
	start := time.Now()

	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, KindUnknown, err
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return time.Since(start), classifyNetwork(err), err
	}
	defer conn.Close()

	if u.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})
		hsCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := tlsConn.HandshakeContext(hsCtx); err != nil {
			return time.Since(start), classifyNetwork(err), err
		}
	}

	return time.Since(start), KindNone, nil
}
