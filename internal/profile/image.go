package profile

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/jpeg" // 仅用于 DecodeConfig 校验真实内容
	_ "image/png"
	"net/http"
)

const (
	maxSide   = 4096
	maxPixels = 8_000_000
)

var imageFormats = map[string]string{"image/png": "png", "image/jpeg": "jpeg"}

// ValidateImage 按真实内容校验素材：只接受 PNG/JPEG/WebP，不接受 SVG、GIF 或任何可执行内容。
// 声明的文件名、扩展名与 Content-Type 均不被信任。
func ValidateImage(role Role, data []byte) (string, error) {
	if len(data) == 0 {
		return "", userErr("%s为空", role.Label())
	}
	if int64(len(data)) > role.MaxBytes() {
		return "", userErr("%s不能超过 %d KiB", role.Label(), role.MaxBytes()>>10)
	}
	mime := http.DetectContentType(data)
	var w, h int
	switch mime {
	case "image/png", "image/jpeg":
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != imageFormats[mime] {
			return "", userErr("%s不是有效的 PNG/JPEG 图片", role.Label())
		}
		w, h = cfg.Width, cfg.Height
	case "image/webp":
		var ok bool
		if w, h, ok = webpSize(data); !ok {
			return "", userErr("%s不是有效的 WebP 图片", role.Label())
		}
	default:
		return "", userErr("%s仅支持 PNG、JPEG、WebP 图片", role.Label())
	}
	if w <= 0 || h <= 0 || w > maxSide || h > maxSide || w*h > maxPixels {
		return "", userErr("%s尺寸 %d×%d 超出限制（单边不超过 %d 像素，总像素不超过 %d 万）", role.Label(), w, h, maxSide, maxPixels/10000)
	}
	return mime, nil
}

// webpSize 解析 RIFF/WebP 头并返回画布尺寸。要求 RIFF 长度与实际长度严格一致，
// 避免在合法图片后夹带额外数据。
func webpSize(d []byte) (int, int, bool) {
	if len(d) < 30 || string(d[0:4]) != "RIFF" || string(d[8:12]) != "WEBP" {
		return 0, 0, false
	}
	if uint64(binary.LittleEndian.Uint32(d[4:8]))+8 != uint64(len(d)) {
		return 0, 0, false
	}
	le24 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }
	switch string(d[12:16]) {
	case "VP8X":
		return 1 + le24(d[24:27]), 1 + le24(d[27:30]), true
	case "VP8L":
		if d[20] != 0x2f {
			return 0, 0, false
		}
		b := d[21:25]
		w := 1 + (int(b[0]) | int(b[1]&0x3f)<<8)
		h := 1 + (int(b[1]>>6) | int(b[2])<<2 | int(b[3]&0x0f)<<10)
		return w, h, true
	case "VP8 ":
		if d[23] != 0x9d || d[24] != 0x01 || d[25] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(d[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(d[28:30]) & 0x3fff), true
	}
	return 0, 0, false
}
