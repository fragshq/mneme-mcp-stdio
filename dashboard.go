package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/fragshq/mneme-mcp/stdio/internal/webserver"
)

// defaultDashboardAddr is where --dashboard=on listens: loopback only,
// on a port as unusual as the compose stack's.
const defaultDashboardAddr = "127.0.0.1:47080"

// dashboardAddr reads the --dashboard setting and returns where to serve the
// dashboard: "" (disabled) when unset or off, defaultDashboardAddr when on,
// and the value itself when it is a host:port.
func dashboardAddr(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "off", "false", "no":
		return "", nil
	case "1", "on", "true", "yes":
		return defaultDashboardAddr, nil
	}
	if _, _, err := net.SplitHostPort(v); err != nil {
		return "", fmt.Errorf("%s=%q: want on, off or host:port", setting(flagDashboard), v)
	}
	return v, nil
}

// startDashboard serves the dashboard on addr and returns a function that
// stops it. Failing to listen (say, the port is taken by another server
// process) is logged, not fatal: the MCP server keeps working.
func startDashboard(addr string, ts webserver.Tenants) (stop func()) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("dashboard not started: %v", err)
		return func() {}
	}
	loopback := ln.Addr().(*net.TCPAddr).IP.IsLoopback() // a "tcp" listener has a TCP address
	e := webserver.New(webserver.Options{Tenants: ts, LoopbackOnly: loopback})
	e.Listener = ln // Start serves on it instead of listening again
	e.Server.ReadHeaderTimeout = 10 * time.Second
	go func() {
		if err := e.Start(""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("dashboard: %v", err)
		}
	}()
	log.Printf("dashboard: http://%s/", ln.Addr())
	if !loopback {
		log.Printf("dashboard: listening beyond loopback; anyone who can reach %s can read and change every tenant's data", ln.Addr())
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = e.Shutdown(ctx)
	}
}
