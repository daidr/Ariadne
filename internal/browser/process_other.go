//go:build !windows

package browser

import "github.com/chromedp/chromedp"

func platformExecOptions() []chromedp.ExecAllocatorOption {
	return nil
}
