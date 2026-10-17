// Package download 是文件内容唯一的受控出口：授权校验、Range、租约与计量。
//
// 契约：
//   - 初次凭证（Claims）默认 300 秒，只决定能否开始新请求；
//   - 活动响应持有独立 30 秒租约，每 10 秒重新校验授权并续租，时钟容限最多 5 秒；
//   - 分享撤销、会话失效或租约过期都会有界地终止活动响应；
//   - 所有字节通过 usage.Stream 先预留后确认，到线时同样终止活动响应。
package download

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"filepanel/internal/files"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

const (
	DefaultTokenTTL   = 300 * time.Second
	DefaultLeaseTTL   = 30 * time.Second
	DefaultRenewEvery = 10 * time.Second
	DefaultSkew       = 5 * time.Second
	defaultChunk      = 32 << 10
)

type Service struct {
	files  *files.Manager
	meter  *usage.Meter
	secret []byte

	Now        func() time.Time
	TokenTTL   time.Duration
	LeaseTTL   time.Duration
	RenewEvery time.Duration
	Skew       time.Duration
	Chunk      int

	mu     sync.Mutex
	leases map[*lease]struct{}
}

func New(s *store.Store, f *files.Manager, m *usage.Meter) (*Service, error) {
	secret, err := loadSecret(s)
	if err != nil {
		return nil, err
	}
	return &Service{files: f, meter: m, secret: secret, Now: time.Now, TokenTTL: DefaultTokenTTL, LeaseTTL: DefaultLeaseTTL,
		RenewEvery: DefaultRenewEvery, Skew: DefaultSkew, Chunk: defaultChunk, leases: map[*lease]struct{}{}}, nil
}

// Error 表示响应开始前即可确定的失败，由调用者渲染。Serve 返回 Error 时不会写任何内容。
type Error struct {
	Status  int
	Code    string
	Message string
	Retry   time.Duration
}

func (e *Error) Error() string { return e.Message }

type Request struct {
	FileID string
	// SHA 非空时必须与当前不可变版本一致。
	SHA      string
	Name     string
	Category string
	IP       string
	// Revalidate 在开始前与每个续租周期调用；返回错误即视为授权已撤销。
	Revalidate func() error
	// Source is an authenticated, streaming remote source. Nil uses local storage.
	Source func(context.Context) (files.Item, ReadSeekCloser, error)
	// Transit traffic is measured at this hop but is not a visitor completion.
	Transit bool
}

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type lease struct {
	file      string
	cancel    context.CancelFunc
	rc        *http.ResponseController
	mu        sync.Mutex
	expires   time.Time
	closer    io.Closer
	closeOnce sync.Once
}

func (l *lease) stop() {
	l.cancel()
	l.closeOnce.Do(func() {
		if l.closer != nil {
			_ = l.closer.Close()
		}
	})
	// 唤醒可能阻塞在慢速客户端上的 Write。
	_ = l.rc.SetWriteDeadline(time.Unix(1, 0))
}

func (l *lease) renew(until time.Time) {
	l.mu.Lock()
	l.expires = until
	l.mu.Unlock()
}

func (l *lease) expired(now time.Time, skew time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return now.After(l.expires.Add(skew))
}

// Revoke 立即终止某文件的全部活动响应（分享关闭或文件删除）。
// 即使调用错过，周期性重新校验也会在一个续租周期内终止它们。
func (s *Service) Revoke(fileID string) int {
	s.mu.Lock()
	var hit []*lease
	for l := range s.leases {
		if l.file == fileID {
			hit = append(hit, l)
		}
	}
	s.mu.Unlock()
	for _, l := range hit {
		l.stop()
	}
	return len(hit)
}

func (s *Service) ActiveLeases() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.leases)
}

func quotaError(err error) *Error {
	switch {
	case errors.Is(err, usage.ErrBusy):
		return &Error{Status: http.StatusTooManyRequests, Code: "busy", Message: err.Error(), Retry: 10 * time.Second}
	case errors.Is(err, usage.ErrIPLimit):
		return &Error{Status: http.StatusTooManyRequests, Code: "ip_limit", Message: err.Error(), Retry: time.Hour}
	case errors.Is(err, usage.ErrQuota):
		return &Error{Status: http.StatusServiceUnavailable, Code: "quota", Message: err.Error(), Retry: time.Hour}
	}
	return &Error{Status: http.StatusInternalServerError, Code: "meter", Message: "计量服务暂不可用，请稍后重试"}
}

// Serve 发送文件的完整内容或一个 Range 区间（HEAD 只发头部）。
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, req Request) error {
	var item files.Item
	var file ReadSeekCloser
	var err error
	if req.Source != nil {
		item, file, err = req.Source(r.Context())
	} else {
		item, file, err = s.files.Open(req.FileID)
	}
	if err != nil {
		return &Error{Status: http.StatusNotFound, Code: "not_found", Message: "文件不存在或不可用"}
	}
	defer file.Close()
	if req.SHA != "" && req.SHA != item.SHA256 {
		return &Error{Status: http.StatusConflict, Code: "changed", Message: "文件版本已变化，请重新获取下载链接"}
	}
	if req.Name != "" {
		item.Name = req.Name
	}
	if req.Revalidate != nil {
		if err := req.Revalidate(); err != nil {
			return &Error{Status: http.StatusForbidden, Code: "revoked", Message: "下载授权已失效，请重新获取下载链接"}
		}
	}
	etag := `"` + item.SHA256 + `"`
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", etag)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Name}))
	h.Set("Cache-Control", "private, no-store")
	rng, partial, rerr := resolveRange(r.Header.Get("Range"), r.Header.Get("If-Range"), etag, item.Size)
	if rerr != nil {
		h.Set("Content-Range", "bytes */"+strconv.FormatInt(item.Size, 10))
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return nil
	}
	status := http.StatusOK
	if !partial {
		rng = byteRange{start: 0, length: item.Size}
	} else {
		status = http.StatusPartialContent
		h.Set("Content-Range", "bytes "+strconv.FormatInt(rng.start, 10)+"-"+strconv.FormatInt(rng.start+rng.length-1, 10)+"/"+strconv.FormatInt(item.Size, 10))
	}
	h.Set("Content-Length", strconv.FormatInt(rng.length, 10))
	if r.Method == http.MethodHead {
		if err := s.meter.Available(req.IP); err != nil {
			h.Del("Content-Length")
			return quotaError(err)
		}
		w.WriteHeader(status)
		return nil
	}
	stream, err := s.meter.Begin(req.IP, req.Category, rng.length)
	if err != nil {
		return quotaError(err)
	}
	completedFull := false
	defer func() { _ = stream.Close(completedFull) }()
	if _, err := file.Seek(rng.start, io.SeekStart); err != nil {
		return &Error{Status: http.StatusInternalServerError, Code: "read", Message: "读取文件失败"}
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	l := &lease{file: req.FileID, cancel: cancel, rc: http.NewResponseController(w), expires: s.Now().Add(s.LeaseTTL), closer: file}
	s.mu.Lock()
	s.leases[l] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.leases, l)
		s.mu.Unlock()
	}()
	done := make(chan struct{})
	var watchdog sync.WaitGroup
	watchdog.Add(1)
	go func() {
		defer watchdog.Done()
		ticker := time.NewTicker(s.RenewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if l.expired(s.Now(), s.Skew) {
					l.stop()
					return
				}
				if req.Revalidate != nil {
					if err := req.Revalidate(); err != nil {
						l.stop()
						return
					}
				}
				if l.expired(s.Now(), s.Skew) {
					l.stop()
					return
				}
				l.renew(s.Now().Add(s.LeaseTTL))
				_ = stream.Flush()
			}
		}
	}()
	defer func() {
		close(done)
		watchdog.Wait()
	}()

	w.WriteHeader(status)
	buf := make([]byte, s.Chunk)
	remaining := rng.length
	for remaining > 0 {
		if ctx.Err() != nil {
			break
		}
		if l.expired(s.Now(), s.Skew) {
			break
		}
		want := int64(len(buf))
		if remaining < want {
			want = remaining
		}
		granted, err := stream.Grant(int(want))
		if err != nil || granted <= 0 {
			break // 到线或预算异常：终止活动响应，客户端可稍后用 Range 续传
		}
		n, readErr := io.ReadFull(file, buf[:granted])
		if n > 0 {
			_ = l.rc.SetWriteDeadline(s.Now().Add(s.LeaseTTL))
			written, writeErr := w.Write(buf[:n])
			stream.Confirm(written)
			remaining -= int64(written)
			if writeErr != nil {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	completedFull = !req.Transit && remaining == 0 && rng.start == 0 && rng.length == item.Size
	return nil
}
