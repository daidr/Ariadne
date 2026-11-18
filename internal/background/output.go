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
func (buffer *outputBuffer) drop(count int) {
	count = min(max(count, 0), buffer.length())
	buffer.head += count
	buffer.dropped += int64(count)
	if buffer.head >= len(buffer.units)/2 {
		buffer.units = append([]uint16(nil), buffer.units[buffer.head:]...)
		buffer.head = 0
	}
}
func (buffer *outputBuffer) take() (string, int64) {
	missed := max(int64(0), buffer.dropped-buffer.read)
	offset := int(max(int64(0), buffer.read-buffer.dropped))
	text := string(utf16.Decode(buffer.units[buffer.head+min(offset, buffer.length()):]))
	buffer.read = buffer.dropped + int64(buffer.length())
	return text, missed
}
func (buffer *outputBuffer) tail(count int) string {
	return string(utf16.Decode(buffer.units[buffer.head+max(0, buffer.length()-count):]))
}
func (buffer *outputBuffer) first(count int) string {
	return string(utf16.Decode(buffer.units[buffer.head : buffer.head+min(count, buffer.length())]))
}
func (buffer *outputBuffer) clear() { buffer.units = nil; buffer.head = 0 }
