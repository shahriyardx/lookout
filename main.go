// Lookout — a single-binary system monitor with a live web UI.
//
// It embeds its entire frontend, collects host metrics via gopsutil, and
// streams them to the browser over Server-Sent Events. One background poller
// serves any number of connected clients.
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"lookout/internal/metrics"
	"lookout/internal/netmgr"
	"lookout/internal/web"
)

//go:embed all:web
var embedded embed.FS

// version is overridable at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	addr := flag.String("addr", envOr("LOOKOUT_ADDR", ":7070"), "listen address")
	interval := flag.Duration("interval", 2*time.Second, "metric refresh interval")
	topN := flag.Int("top", 12, "number of top processes to report")
	dockerSock := flag.String("docker-socket", detectDockerSocket(), "docker socket path (empty to disable)")
	nmEnabled := flag.Bool("netmgr", true, "show the NetworkManager control tab when NM is present")
	terminal := flag.Bool("terminal", true, "web terminal tab (a full remote shell); enabled by default — pass -terminal=false to disable")
	terminalCmd := flag.String("terminal-cmd", "", "shell argv for the terminal; default: $SHELL or /bin/sh")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("lookout %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	assets, err := fs.Sub(embedded, "web")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	web.Version = version
	col := metrics.New(*topN, *dockerSock)

	// Auto-detect NetworkManager; nil when absent (or disabled) hides the tab.
	var nm *netmgr.Manager
	if *nmEnabled {
		nm = netmgr.New()
	}

	srv := web.New(col, nm, *interval, assets)

	if *terminal {
		srv.EnableTerminal(shellArgv(*terminalCmd))
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("lookout %s listening on http://localhost%s (interval %s, docker=%v, netmgr=%v, terminal=%v)",
			version, *addr, *interval, col.DockerEnabled(), nm.Available(), *terminal)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// shellArgv resolves the terminal's shell argv: an explicit -terminal-cmd
// (space-split), else $SHELL, else the first of bash/sh that exists.
func shellArgv(cmd string) []string {
	if f := strings.Fields(cmd); len(f) > 0 {
		return f
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return []string{sh}
	}
	for _, c := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(c); err == nil {
			return []string{c}
		}
	}
	return []string{"/bin/sh"}
}

// detectDockerSocket returns the first Docker socket that exists, or "" if none.
func detectDockerSocket() string {
	if v := os.Getenv("LOOKOUT_DOCKER_SOCKET"); v != "" {
		return v
	}
	candidates := []string{
		"/var/run/docker.sock",
		os.Getenv("HOME") + "/.docker/run/docker.sock",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if fi, err := os.Stat(c); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return c
		}
	}
	return ""
}
