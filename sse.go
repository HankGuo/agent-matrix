package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sseBroker 是管理端实时事件的分发中心。订阅者各持一条带缓冲的 channel，
// 发布为非阻塞投递：慢消费者丢事件无妨，前端还有兜底轮询自愈。
type sseBroker struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func newSSEBroker() *sseBroker {
	return &sseBroker{subs: make(map[chan string]struct{})}
}

func (b *sseBroker) subscribe() chan string {
	ch := make(chan string, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *sseBroker) unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (b *sseBroker) publish(topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- topic:
		default: // 缓冲满了直接丢，下一次状态拉取会补齐
		}
	}
}

// publish 是 nil 安全的广播入口：测试里的 server 不装配 broker。
func (s *server) publish(topic string) {
	if s.broker != nil {
		s.broker.publish(topic)
	}
}

// handleEvents 是管理端的 SSE 长连接。EventSource 同源自带会话 Cookie，
// 经 requireAdmin 认证后挂载；事件体极简（仅 topic），数据仍走既有 JSON 接口拉取。
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // 反代有缓冲时也要即时下发
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": hello\n\n")
	flusher.Flush()

	ch := s.broker.subscribe()
	defer s.broker.unsubscribe(ch)

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case topic := <-ch:
			_, _ = fmt.Fprintf(w, "data: {\"topic\":%q}\n\n", topic)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
