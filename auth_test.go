package main

import (
	"testing"
	"time"
)

// TestRateLimiterSweep hits 达到容量兜底时全量清扫过期 key，
// 防止海量一次性来源的 key 永久滞留内存。
func TestRateLimiterSweep(t *testing.T) {
	l := newRateLimiter(5, time.Minute)

	// 填满 rateKeyCap 个全部已过期的 key（时间戳在窗口之外）
	stale := time.Now().Add(-2 * time.Minute)
	for i := 0; i < rateKeyCap; i++ {
		l.hits[string(rune(i))+"-stale"] = []time.Time{stale}
	}
	// 一个仍在窗口内的 key，清扫后必须存活
	l.hits["recent"] = []time.Time{time.Now()}

	if !l.allow("fresh") {
		t.Fatal("清扫后新 key 应被放行")
	}
	if len(l.hits) != 2 { // recent + fresh
		t.Fatalf("过期 key 应被全量清扫，剩余 %d 个", len(l.hits))
	}
	if _, ok := l.hits["recent"]; !ok {
		t.Fatal("窗口内的 key 不应被清扫")
	}
}

// TestRateLimiterLimit 窗口内超过次数即拒绝，窗口过后恢复。
func TestRateLimiterLimit(t *testing.T) {
	l := newRateLimiter(2, time.Minute)
	for i := 0; i < 2; i++ {
		if !l.allow("k") {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if l.allow("k") {
		t.Fatal("超过限额应拒绝")
	}
	// 手动把记录推到窗口之外，等价于时间流逝
	l.hits["k"] = []time.Time{time.Now().Add(-2 * time.Minute)}
	if !l.allow("k") {
		t.Fatal("窗口过后应恢复放行")
	}
}
