package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClean 控制字符剔除 + 按字符数截断（不得切断多字节 UTF-8 字符）。
func TestClean(t *testing.T) {
	if got := clean("  hello\x00\x07\x1fworld  ", 100); got != "helloworld" {
		t.Fatalf("控制字符与首尾空白应被剔除: %q", got)
	}

	long := strings.Repeat("汉", 10)
	got := clean(long, 4)
	if got != "汉汉汉汉" {
		t.Fatalf("多字节截断应为 4 个完整汉字: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果必须是合法 UTF-8: %q", got)
	}

	// 长度恰好等于上限时不截断
	if got := clean("abc", 3); got != "abc" {
		t.Fatalf("恰好等于上限不应截断: %q", got)
	}
}
