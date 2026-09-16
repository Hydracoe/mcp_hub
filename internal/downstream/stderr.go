package downstream

import (
	"bytes"
	"sync"
)

// maxStderrBytes 是 stderr 保留上限。stdio 失败时 stderr 是唯一定位线索，
// 但流可能很长，只留最近的输出。
const maxStderrBytes = 4 << 10

// stderrSink 捕获子进程 stderr 的尾部内容，实现 io.Writer。
//
// 用 WithCommandStderrWriter 注入而不是读 GetStderr：后者返回的 reader
// 在进程存活时可能阻塞。
type stderrSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *stderrSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.buf.Write(p)

	if s.buf.Len() > maxStderrBytes {
		tail := s.buf.Bytes()[s.buf.Len()-maxStderrBytes:]
		kept := make([]byte, len(tail))
		copy(kept, tail)
		s.buf.Reset()
		s.buf.Write(kept)
	}

	return len(p), nil
}

// String 返回已捕获的 stderr，去除首尾空白。
func (s *stderrSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(bytes.TrimSpace(s.buf.Bytes()))
}
