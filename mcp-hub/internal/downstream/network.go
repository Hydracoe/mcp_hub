package downstream

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"time"
)

// probeNetwork 执行 L1 可达性探测：DNS → TCP → TLS。
//
// 刻意不发 HTTP 请求，只确认"能不能连上"。好处是错误类型干净：失败时拿到的是
// *net.DNSError、*tls.CertificateVerificationError 这类结构化错误，而不是被上层
// 库压平的字符串。HTTP 语义（401/404/5xx）需要真的发请求，由 Recorder 在 L2/L3 捕获。
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
