//go:build !windows

package fsutil

import "context"

func MeasureReFSExclusive(context.Context, string) *int64 { return nil }
