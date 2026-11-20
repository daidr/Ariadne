package background

import "unicode/utf16"

type outputBuffer struct {
	units         []uint16
	head          int
	dropped, read int64
}

func (buffer *outputBuffer) length() int { return len(buffer.units) - buffer.head }
func (buffer *outputBuffer) append(text string) {
	trimmed := false
	for _, r := range text {
		if r > 0xffff {
			a, b := utf16.EncodeRune(r)
			buffer.units = append(buffer.units, uint16(a), uint16(b))
		} else {
			buffer.units = append(buffer.units, uint16(r))
		}
		if buffer.length() > keptOutput*5/4 {
			buffer.drop(buffer.length() - keptOutput)
			trimmed = true
		}
	}
	if trimmed && buffer.length() > keptOutput {
		buffer.drop(buffer.length() - keptOutput)
	}
}
func (buffer *outputBuffer) drop(count int) int {
	count = min(max(count, 0), buffer.length())
	if boundary := buffer.head + count; count > 0 && boundary < len(buffer.units) && buffer.units[boundary] >= 0xdc00 && buffer.units[boundary] <= 0xdfff {
		count++
	}
	buffer.head += count
	buffer.dropped += int64(count)
	if buffer.head >= len(buffer.units)/2 {
		buffer.units = append([]uint16(nil), buffer.units[buffer.head:]...)
		buffer.head = 0
	}
	return count
}
func (buffer *outputBuffer) take(limit int) (string, int64, bool) {
	missed := max(int64(0), buffer.dropped-buffer.read)
	offset := int(max(int64(0), buffer.read-buffer.dropped))
	start, end := buffer.head+min(offset, buffer.length()), len(buffer.units)
	if limit > 0 && end-start > max(2, limit) {
		end = start + max(2, limit)
		if buffer.units[end] >= 0xdc00 && buffer.units[end] <= 0xdfff {
			end--
		}
	}
	text := string(utf16.Decode(buffer.units[start:end]))
	buffer.read = buffer.dropped + int64(end-buffer.head)
	return text, missed, end < len(buffer.units)
}
func (buffer *outputBuffer) tail(count int) string {
	return string(utf16.Decode(buffer.units[buffer.head+max(0, buffer.length()-count):]))
}
func (buffer *outputBuffer) first(count int) string {
	return string(utf16.Decode(buffer.units[buffer.head : buffer.head+min(count, buffer.length())]))
}
func (buffer *outputBuffer) clear() { buffer.units = nil; buffer.head = 0 }
