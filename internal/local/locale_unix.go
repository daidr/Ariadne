//go:build !windows

package local

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/daidr/Ariadne/internal/process"
)

func defaultLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result := process.Run(ctx, []string{"/usr/bin/defaults", "read", "-g", "AppleLocale"}, process.RunOptions{})
		if result.ExitCode == 0 {
			return result.Stdout
		}
	}
	return "en-US"
}
