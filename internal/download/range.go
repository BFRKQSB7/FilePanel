package download

import (
	"errors"
	"strconv"
	"strings"
)

var errUnsatisfiable = errors.New("范围无法满足")

// byteRange 是闭区间在文件内的绝对位置。
type byteRange struct {
	start, length int64
}

// resolveRange 解析 Range / If-Range。
//   - 没有 Range，或 If-Range 与当前 ETag 不一致：返回 ok=false，调用者发送完整 200；
//   - 非法、非 bytes、多段或越界：返回 errUnsatisfiable（416）；
//   - 其余只返回一个区间，调用者不会为小范围读取全文件。
//
// If-Range 只接受强 ETag 比较；HTTP 日期形式一律视为不匹配，从而安全地回退为完整响应。
func resolveRange(header, ifRange, etag string, size int64) (r byteRange, ok bool, err error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return r, false, nil
	}
	if ifRange = strings.TrimSpace(ifRange); ifRange != "" && ifRange != etag {
		return r, false, nil
	}
	const prefix = "bytes="
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return r, false, errUnsatisfiable
	}
	spec := strings.TrimSpace(header[len(prefix):])
	if spec == "" || strings.Contains(spec, ",") {
		return r, false, errUnsatisfiable
	}
	first, last, found := strings.Cut(spec, "-")
	if !found || size <= 0 {
		return r, false, errUnsatisfiable
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if first == "" {
		n, ok := parseDigits(last)
		if !ok || n == 0 {
			return r, false, errUnsatisfiable
		}
		if n > size {
			n = size
		}
		return byteRange{start: size - n, length: n}, true, nil
	}
	start, okStart := parseDigits(first)
	if !okStart || start >= size {
		return r, false, errUnsatisfiable
	}
	end := size - 1
	if last != "" {
		v, okEnd := parseDigits(last)
		if !okEnd || v < start {
			return r, false, errUnsatisfiable
		}
		if v < end {
			end = v
		}
	}
	return byteRange{start: start, length: end - start + 1}, true, nil
}

func parseDigits(s string) (int64, bool) {
	if s == "" || len(s) > 18 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}
