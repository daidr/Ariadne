package rpc

import "regexp"

var passing = regexp.MustCompile(`(?i)overload|rate.?limit|too many requests|\b429\b|\b5[0-9][0-9]\b|capacity|temporar|timed? ?out|ECONNRESET|socket hang up`)

var quota = regexp.MustCompile(`(?i)\bquota[_ ]?(reached|exceeded|exhausted|depleted|limit)\b|\bquota\b[^\n]*(reached|exceeded|exhausted|depleted|limit|reset)|(?:reached|exceeded|exhausted)[^\n]*\b(quota|usage limit|subscription limit|plan limit|spending limit)\b|\b(usage|subscription|plan|credit|daily|weekly|monthly|individual|team|organization|enterprise|model)\s+(quota|limit)\s+(reached|exceeded|exhausted)\b|\b(quota|limit)\b[^\n]*\bresets?\s+(in|at|after)\b|insufficient_quota|upgrade your subscription`)

func BusyText(text string) bool { return passing.MatchString(text) }

func QuotaText(text string) bool { return quota.MatchString(text) }
