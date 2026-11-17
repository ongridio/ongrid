package llm

import (
	"bytes"
	"encoding/binary"
)

// ImageDimensions reads PNG, JPEG and WebP headers without decoding pixels.
func ImageDimensions(data []byte, mimeType string) (int, int, bool) {
	if mimeType == "image/png" {
		if len(data) < 24 || !bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
			return 0, 0, false
		}
		return int(binary.BigEndian.Uint32(data[16:20])), int(binary.BigEndian.Uint32(data[20:24])), true
	}
	if mimeType == "image/webp" {
		return webPDimensions(data)
	}
	if mimeType != "image/jpeg" || len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 0, 0, false
	}
	for offset := 2; offset+9 < len(data); {
		if data[offset] != 0xff {
			offset++
			continue
		}
		marker := data[offset+1]
		offset += 2
		if marker == 0xd8 || marker == 0xd9 {
			continue
		}
		if offset+2 > len(data) {
			break
		}
		length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if length < 2 || offset+length > len(data) {
			break
		}
		if marker >= 0xc0 && marker <= 0xc3 && length >= 7 {
			return int(binary.BigEndian.Uint16(data[offset+5 : offset+7])), int(binary.BigEndian.Uint16(data[offset+3 : offset+5])), true
		}
		offset += length
	}
	return 0, 0, false
}

func webPDimensions(data []byte) (int, int, bool) {
	if len(data) < 30 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(data[12:16]) {
	case "VP8X":
		width := 1 + int(data[24]) + int(data[25])<<8 + int(data[26])<<16
		height := 1 + int(data[27]) + int(data[28])<<8 + int(data[29])<<16
		return width, height, true
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, true
	case "VP8 ":
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		width := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
		return width, height, true
	default:
		return 0, 0, false
	}
}
