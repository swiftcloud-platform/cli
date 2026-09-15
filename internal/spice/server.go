package spice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Open starts a local HTTP server with the embedded SPICE client and opens
// the user's browser to it. It blocks until the server is shut down.
//
// platformWsURL is the WebSocket URL for the platform's console proxy
// (returned by the /console API endpoint). ticket is the short-lived
// authentication ticket.
func Open(platformWsURL, ticket string) error {
	mux := http.NewServeMux()

	// Serve the SPICE client JS
	mux.HandleFunc("/spice-client.min.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(jsBundle)
	})

	// Serve the viewer page — connection params are embedded by the handler.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(indexHTML))
	})

	// WebSocket proxy — the browser connects here, we bridge to the platform.
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		proxyWebSocket(w, r, platformWsURL, ticket)
	})

	// Find a free port on loopback
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("binding local port: %w", err)
	}
	defer func() { _ = listener.Close() }()

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

	// Open the browser — the SPICE client reads ticket from the query param.
	consoleURL := baseURL + "/?ticket=" + url.QueryEscape(ticket)
	if err := openBrowser(consoleURL); err != nil {
		_ = srv.Close()
		return fmt.Errorf("opening browser: %w", err)
	}

	// Wait for the server to stop (Ctrl-C or error)
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("local server: %w", err)
	}
	return nil
}

// proxyWebSocket bridges a browser WebSocket to the platform's console proxy.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, platformWsURL, ticket string) {
	// Upgrade the browser connection.
	browserWs, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		return
	}

	// Connect to the platform's console WebSocket with the ticket.
	platformURL := platformWsURL
	if !strings.Contains(platformURL, "?") {
		platformURL += "?"
	} else {
		platformURL += "&"
	}
	platformURL += "ticket=" + url.QueryEscape(ticket)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	platformWs, _, err := websocket.Dial(ctx, platformURL, nil)
	cancel()
	if err != nil {
		_ = browserWs.Close(websocket.StatusInternalError, "platform connection failed")
		return
	}

	// Bridge both directions.
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = browserWs.CloseNow()
			_ = platformWs.CloseNow()
		})
	}

	// Browser → Platform
	go func() {
		defer closeBoth()
		for {
			typ, data, err := browserWs.Read(r.Context())
			if err != nil {
				return
			}
			if err := platformWs.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}()

	// Platform → Browser
	go func() {
		defer closeBoth()
		for {
			typ, data, err := platformWs.Read(r.Context())
			if err != nil {
				return
			}
			if err := browserWs.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}()

	// Wait until one side closes or errors, then tear down.
	<-r.Context().Done()
	closeBoth()
	_ = platformWs.Close(websocket.StatusNormalClosure, "")
	_ = browserWs.Close(websocket.StatusNormalClosure, "")
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

	c := exec.Command(cmd, args...) // #nosec G204 -- command is a fixed browser launcher
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
