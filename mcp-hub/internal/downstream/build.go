package downstream

import (
	"fmt"
	"io"
	"net/http"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
)

// 网关作为 MCP 客户端时对外声明的身份。
const (
	ClientName    = "mcp-hub"
	ClientVersion = "0.1.0"
)

// buildClient 按传输类型构造下游 client，但不启动（启动交给探活或 Conn，以便套上超时）。
// 自行组合 transport + NewClient 而非便捷构造函数，才能注入 ClientOption（如协议版本偏好）。
// hdrFn 非空时改为每请求现场取凭据，这是密钥轮换的前提；探活传 nil 走静态 headers。
func buildClient(spec Spec, rec *Recorder, stderr io.Writer, hdrFn transport.HTTPHeaderFunc) (*mcpclient.Client, error) {
	clientOpts := make([]mcpclient.ClientOption, 0, 2)
	if spec.ProtocolVersion != "" {
		clientOpts = append(clientOpts, mcpclient.WithProtocolVersion(spec.ProtocolVersion))
	}
	if spec.LegacyOnly {
		clientOpts = append(clientOpts, mcpclient.WithLegacyProtocolOnly())
	}

	switch spec.Transport {
	case TransportStreamableHTTP:
		httpOpts := []transport.StreamableHTTPCOption{
			transport.WithHTTPBasicClient(&http.Client{Transport: rec}),
			transport.WithHTTPTimeout(spec.TimeoutOr()),
		}
		switch {
		case hdrFn != nil:
			httpOpts = append(httpOpts, transport.WithHTTPHeaderFunc(hdrFn))
		case len(spec.Headers) > 0:
			httpOpts = append(httpOpts, transport.WithHTTPHeaders(spec.Headers))
		}
		trans, err := transport.NewStreamableHTTP(spec.URL, httpOpts...)
		if err != nil {
			return nil, err
		}
		return mcpclient.NewClient(trans, clientOpts...), nil

	case TransportSSE:
		sseOpts := []transport.ClientOption{
			transport.WithHTTPClient(&http.Client{Transport: rec}),
			transport.WithResponseTimeout(spec.TimeoutOr()),
		}
		switch {
		case hdrFn != nil:
			sseOpts = append(sseOpts, transport.WithHeaderFunc(hdrFn))
		case len(spec.Headers) > 0:
			sseOpts = append(sseOpts, transport.WithHeaders(spec.Headers))
		}
		trans, err := transport.NewSSE(spec.URL, sseOpts...)
		if err != nil {
			return nil, err
		}
		return mcpclient.NewClient(trans, clientOpts...), nil

	case TransportStdio:
		stdioOpts := make([]transport.StdioOption, 0, 1)
		if stderr != nil {
			stdioOpts = append(stdioOpts, transport.WithCommandStderrWriter(stderr))
		}
		trans := transport.NewStdioWithOptions(spec.Command, spec.Env, spec.Args, stdioOpts...)
		return mcpclient.NewClient(trans, clientOpts...), nil
	}

	return nil, fmt.Errorf("unsupported transport %q", spec.Transport)
}
