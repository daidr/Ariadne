package rpc

import "regexp"

var passing = regexp.MustCompile(`(?i)overload|rate.?limit|too many requests|\b429\b|\b5[0-9][0-9]\b|capacity|temporar|timed? ?out|ECONNRESET|socket hang up`)

func BusyText(text string) bool { return passing.MatchString(text) }
