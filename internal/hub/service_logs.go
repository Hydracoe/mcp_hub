package hub

import (
	"context"
	"time"

	"github.com/hydra/mcp-hub/internal/hub/aggregate"
	"github.com/hydra/mcp-hub/internal/store/logs"
	"go.uber.org/zap"
)

// enqueueLog 把调用日志投入异步队列。
// 队列满时直接丢弃而不是阻塞：日志是观测手段，不该成为调用路径上的
// 一个可用性依赖。
func (s *Service) enqueueLog(rec aggregate.LogRecord) {
	entry := logs.LogEntry{
		ServerID:     rec.ServerID,
		ServerKey:    rec.ServerKey,
		ToolName:     rec.ToolName,
		ExposedName:  rec.Exposed,
		DurationMS:   rec.Duration.Milliseconds(),
		Status:       rec.Status,
		ErrorKind:    rec.Kind,
		Error:        rec.Err,
		CredentialID: rec.CredentialID,
		APIKeyID:     rec.APIKeyID,
		APIKeyName:   rec.APIKeyName,
		Args:         rec.Args,
		Result:       rec.Result,
	}

	select {
	case s.logCh <- entry:
	case <-s.closeCh:
	default:
		s.log.Debug("调用日志队列已满，丢弃一条记录", zap.String("tool", rec.Exposed))
	}
}

// logWorker 批量落库，摊薄数据库的写事务开销。
func (s *Service) logWorker() {
	defer s.closeWG.Done()

	const (
		batchSize    = 32
		flushTimeout = 500 * time.Millisecond
	)

	batch := make([]logs.LogEntry, 0, batchSize)
	ticker := time.NewTicker(flushTimeout)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, e := range batch {
			if err := s.store.Logs.Append(ctx, e); err != nil {
				s.log.Error("写入调用日志失败", zap.Error(err))
				break
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-s.closeCh:
			// 退出前把队列里剩下的日志冲干净。
		drain:
			for {
				select {
				case e := <-s.logCh:
					batch = append(batch, e)
				default:
					break drain
				}
			}
			flush()
			return

		case e := <-s.logCh:
			batch = append(batch, e)
			if len(batch) >= batchSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}
