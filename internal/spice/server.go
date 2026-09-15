package spice

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Open starts a local HTTP server with the embedded SPICE client and opens
// the user's browser to it. It blocks until the server is shut down.
//
// wsURL is the WebSocket URL for the platform's SPICE proxy.
// ticket is the short-lived authentication ticket from the API.
func Open(wsURL, ticket string) error {
	mux := http.NewServeMux()

	// Serve the SPICE client JS
	mux.HandleFunc("/spice-client.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(jsBundle)
	})

	// Serve the viewer page — connection params are passed as query strings
	// and read by JavaScript in the page.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})

	// Find a free port on loopback
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("binding local port: %w", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()
	baseURL := "http://" + addr

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Start the server in the background
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(listener)
	}()

	// Build the URL with query params
	q := url.Values{}
	q.Set("ws", wsURL)
	q.Set("ticket", ticket)
	consoleURL := baseURL + "?" + q.Encode()

	// Open the browser
	if err := openBrowser(consoleURL); err != nil {
		srv.Close()
		return fmt.Errorf("opening browser: %w", err)
	}

	// Wait for the server to stop (Ctrl-C or error)
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("local server: %w", err)
		}
	}
	return nil
}

// openBrowser opens the default browser. No-op if the command is not found.
func openBrowser(target string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", "", target}
	case "darwin":
		cmd = "open"
		args = []string{target}
	default: // linux, freebsd, etc.
		if isWSL() {
			cmd = "cmd.exe"
			args = []string{"/c", "start", "", target}
		} else {
			cmd = "xdg-open"
			args = []string{target}
		}
	}

	c := exec.Command(cmd, args...)
	c.Stdin = nil
	c.Stdout = nil
	c.Stderr = nil
	return c.Start()
}

// isWSL detects Windows Subsystem for Linux.
func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	release, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(release)), "microsoft")
}
