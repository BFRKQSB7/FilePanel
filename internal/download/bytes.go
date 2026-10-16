package download

import (
	"net/http"
	"strconv"
)

// ServeBytes 发送一个小型内存对象（图片素材），同样占用并发名额并计入用量。
func (s *Service) ServeBytes(w http.ResponseWriter, r *http.Request, ip, category, contentType string, data []byte) error {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=60")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		if err := s.meter.Available(ip); err != nil {
			h.Del("Content-Length")
			return quotaError(err)
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}
	stream, err := s.meter.Begin(ip, category, int64(len(data)))
	if err != nil {
		h.Del("Content-Length")
		return quotaError(err)
	}
	complete := false
	defer func() { _ = stream.Close(complete) }()
	w.WriteHeader(http.StatusOK)
	for off := 0; off < len(data); {
		granted, err := stream.Grant(len(data) - off)
		if err != nil || granted <= 0 {
			return nil
		}
		n, werr := w.Write(data[off : off+granted])
		stream.Confirm(n)
		off += n
		if werr != nil {
			return nil
		}
	}
	return nil
}
