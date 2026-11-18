package main

import (
	"os"
	"os/exec"
	"runtime"
)

func openBrowser(pageURL string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", pageURL)
	case "darwin":
		command = exec.Command("open", pageURL)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return
		}
		command = exec.Command("xdg-open", pageURL)
	}
	hideWindow(command)
	if command.Start() == nil {
		go func() { _ = command.Wait() }()
	}
}
