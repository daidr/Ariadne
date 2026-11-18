package server

import (
	"regexp"
	"strconv"

	"github.com/daidr/Ariadne/internal/textutil"
)

// Query parameters previously passed through JavaScript Number(), including
// whitespace and unsigned base-prefixed integers.
var decimalNumber = regexp.MustCompile(`^[+-]?(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?|Infinity)$`)

func queryNumber(raw string) (float64, error) {
	value := textutil.Trim(raw)
	if value == "" {
		return 0, nil
	}
	if len(value) > 2 && value[0] == '0' {
		base := 0
		switch value[1] {
		case 'x', 'X':
			base = 16
		case 'b', 'B':
			base = 2
		case 'o', 'O':
			base = 8
		}
		if base != 0 {
			number, err := strconv.ParseUint(value[2:], base, 64)
			return float64(number), err
		}
	}
	if !decimalNumber.MatchString(value) {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseFloat(value, 64)
}
