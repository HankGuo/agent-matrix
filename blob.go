package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// 附件存储边界：单文件 100MB、每任务 10 个。不设全局配额——磁盘实际容量即上限。
const (
	maxAttachmentSize     = 100 << 20 // 100MB
	maxAttachmentsPerTask = 10
)

var errAttTooLarge = fmt.Errorf("单个附件不能超过 %dMB", maxAttachmentSize>>20)

// blobStore 是附件字节存储的抽象，当前仅 local 驱动：落盘本机目录。
type blobStore interface {
	// Put 流式写入，先落临时文件再 rename（崩溃不留半截文件）；返回实际大小与嗅探出的 MIME。
	Put(key string, r io.Reader) (size int64, mime string, err error)
	// Open 打开一个已存在的对象。
	Open(key string) (*os.File, int64, error)
	// Delete 删除对象，不存在不视为错误。
	Delete(key string) error
}

// localBlob 把对象存在固定目录下，文件名为 key（key 由服务端生成，可防路径穿越）。
type localBlob struct{ dir string }

var blobKeyRE = regexp.MustCompile(`^att_[0-9a-f]{16}$`)

func newLocalBlob(dir string) (*localBlob, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &localBlob{dir: dir}, nil
}

func (b *localBlob) path(key string) (string, error) {
	if !blobKeyRE.MatchString(key) {
		return "", errors.New("非法的存储键")
	}
	return filepath.Join(b.dir, key), nil
}

func (b *localBlob) Put(key string, r io.Reader) (int64, string, error) {
	p, err := b.path(key)
	if err != nil {
		return 0, "", err
	}
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", err
	}
	size, err := io.Copy(f, io.LimitReader(r, maxAttachmentSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, "", err
	}
	if size > maxAttachmentSize {
		os.Remove(tmp)
		return 0, "", errAttTooLarge
	}
	// 不信任客户端声明的 Content-Type：取前 512 字节嗅探，预览决策用嗅探值。
	mime := "application/octet-stream"
	if hf, err := os.Open(tmp); err == nil {
		buf := make([]byte, 512)
		if n, _ := hf.Read(buf); n > 0 {
			mime = http.DetectContentType(buf[:n])
		}
		hf.Close()
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return 0, "", err
	}
	return size, mime, nil
}

func (b *localBlob) Open(key string) (*os.File, int64, error) {
	p, err := b.path(key)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, errAttNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (b *localBlob) Delete(key string) error {
	p, err := b.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
