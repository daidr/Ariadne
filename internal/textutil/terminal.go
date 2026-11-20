package textutil

import "bytes"

// TerminalEscapes removes ANSI sequences from consecutive chunks of one UTF-8 stream.
// Each stdout/stderr stream needs its own filter.
type TerminalEscapes struct{ state byte }

const (
	terminalText byte = iota
	terminalEscape
	terminalIntermediate
	terminalCSI
	terminalString
	terminalStringEnd
)

func (filter *TerminalEscapes) Strip(data []byte) []byte {
	if filter.state == terminalText && bytes.IndexByte(data, 0x1b) < 0 {
		return data
	}
	clean := make([]byte, 0, len(data))
	for _, b := range data {
		switch filter.state {
		case terminalText:
			if b == 0x1b {
				filter.state = terminalEscape
			} else {
				clean = append(clean, b)
			}
		case terminalEscape:
			switch {
			case b == '[':
				filter.state = terminalCSI
			case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
				filter.state = terminalString
			case b >= 0x20 && b <= 0x2f:
				filter.state = terminalIntermediate
			case b != 0x1b:
				filter.state = terminalText
				if b < 0x20 || b > 0x7e {
					clean = append(clean, b)
				}
			}
		case terminalIntermediate, terminalCSI:
			switch {
			case b == 0x1b:
				filter.state = terminalEscape
			case b >= 0x40 && b <= 0x7e || filter.state == terminalIntermediate && b >= 0x30 && b <= 0x3f:
				filter.state = terminalText
			case b < 0x20 || b > 0x7e:
				filter.state = terminalText
				clean = append(clean, b)
			}
		case terminalString:
			switch b {
			case 0x07:
				filter.state = terminalText
			case 0x1b:
				filter.state = terminalStringEnd
			}
		case terminalStringEnd:
			if b == '\\' || b == 0x07 {
				filter.state = terminalText
			} else if b != 0x1b {
				filter.state = terminalString
			}
		}
	}
	return clean
}
