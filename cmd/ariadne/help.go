package main

import (
	"fmt"
	"io"
)

func printRootHelp(w io.Writer) {
	fmt.Fprintf(w, `Ariadne - AI-native collaboration workspace

Usage:
  ariadne <command> [flags]
  ard <command> [flags]

Commands:
  dashboard       Print the authenticated dashboard URL
  service         Manage the background Ariadne service (start, stop, restart, status, logs, install, uninstall)
  upgrade         Check for and install Ariadne updates without interrupting active sessions
  serve           Run the Ariadne server in the foreground
  version         Print the Ariadne version
  help [command]  Show help for a command

Flags:
  -h, --help      Show help
  -v, --version   Print the Ariadne version

Run "ariadne <command> --help" for more information about a command.
`)
}

func printServeHelp(w io.Writer) {
	fmt.Fprintf(w, `Run the Ariadne server in the foreground.

Usage:
  ariadne serve [flags]

Flags:
  --host <addr>         Address to bind (default: ARIADNE_HOST or 127.0.0.1)
  --port <port>         Port to listen on (default: ARIADNE_PORT or 7428)
  --home <dir>          Ariadne data directory (default: ARIADNE_HOME or ~/.ariadne)
  --open                Open the dashboard in a browser after startup
  --no-open             Do not open a browser after startup
  --log-file <path>     Write rotating server logs to this file
  -h, --help            Show help
`)
}

func printDashboardHelp(w io.Writer) {
	fmt.Fprintf(w, `Print the authenticated Ariadne dashboard URL.

Usage:
  ariadne dashboard [flags]

Flags:
  --open                Open the dashboard in the default browser
  --json                Output connection details as JSON
  --home <dir>          Ariadne data directory (default: ARIADNE_HOME or ~/.ariadne)
  -h, --help            Show help
`)
}

func printServiceHelp(w io.Writer) {
	fmt.Fprintf(w, `Manage the Ariadne user-level background service.

Usage:
  ariadne service <subcommand> [flags]

Subcommands:
  install       Register the user-level background service (launchd / systemd / Windows logon task)
  uninstall     Stop and remove the background service registration
  start         Start the background service and wait until ready
  stop          Safely drain active sessions and stop the running server
  restart       Safely drain active sessions and restart the running server
  status        Show service state, active AI sessions, and background commands
  logs          Show recent service logs (use -f to follow)

Common flags:
  --home <dir>          Ariadne data directory (default: ARIADNE_HOME or ~/.ariadne)
  --host <addr>         Bind address (for install / start)
  --port <port>         Listen port (for install / start)
  --autostart           Enable start at login (default for install)
  --no-autostart        Disable start at login (for install)
  --start               Also start the service right after install
  --mode <mode>         Drain mode for stop/restart: safe-point (default) or turn
  --when-idle           Schedule stop/restart automatically once all sessions are idle
  --wait-background     Also wait for active background commands before stopping/restarting
  --timeout <duration>  Max wait before cancelling drain (default: 5m)
  --force               Stop/restart immediately without waiting for active sessions
  --json                Output status as JSON
  -f, --follow          Follow log output (for logs)
  -n, --lines <count>   Number of log lines to show (default: 100)
  -h, --help            Show help
`)
}

func printUpgradeHelp(w io.Writer) {
	fmt.Fprintf(w, `Check for updates, install the latest release, and safely restart Ariadne.

Usage:
  ariadne upgrade [flags]

Flags:
  --check               Check whether a newer version is available without installing
  --channel <name>      Release channel: stable or preview
  --version <ver>       Install a specific release version (for example, v0.1.0)
  --mode <mode>         Drain mode when restarting: safe-point (default) or turn
  --when-idle           Replace binary now and restart automatically once idle
  --wait-background     Also wait for active background commands before restarting
  --timeout <duration>  Max wait for active sessions before cancelling restart (default: 5m)
  --force               Restart immediately after replacing the binary
  --no-restart          Replace the binary without restarting the running service
  --home <dir>          Ariadne data directory (default: ARIADNE_HOME or ~/.ariadne)
  -h, --help            Show help
`)
}

func printVersionHelp(w io.Writer) {
	fmt.Fprintf(w, `Print the Ariadne version.

Usage:
  ariadne version [--json]
  ariadne --version
`)
}
