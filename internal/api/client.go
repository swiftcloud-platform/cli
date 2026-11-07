// Package api is the generated platform client plus the error handling around
// it: RFC 9457 problem documents become typed errors whose Type the command
// layer maps to an exit code.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud/internal/termsafe"
)

/*
Hand-written companions to the generated client (gen.go).

The generated code knows the wire; this file knows the CLI: it attaches the
credential and User-Agent, refuses requests and responses the CLI must not act
on, and turns the platform's RFC 9457 problem responses into a typed error the
commands can map to an exit code and a sentence.
*/

// Error is a problem+json response from the platform.
type Error struct {
	Status int
	// Type is the last segment of the problem type URL: unauthenticated,
	// forbidden, not-found, validation, conflict, rate-limited, internal.
	Type   string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return termsafe.String(e.Detail)
	}
	return fmt.Sprintf("the platform returned HTTP %d", e.Status)
}

// Is lets callers match on type: errors.Is(err, api.ErrNotFound).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Type == e.Type
}

// Sentinel errors for the problem types the CLI branches on. Compare with
// errors.Is; the Detail carried by a real response is preserved separately.
var (
	ErrUnauthenticated = &Error{Type: "unauthenticated"}
	ErrForbidden       = &Error{Type: "forbidden"}
	ErrNotFound        = &Error{Type: "not-found"}
	ErrValidation      = &Error{Type: "validation"}
	ErrConflict        = &Error{Type: "conflict"}
)

// ErrorFromResponse builds an *Error from a non-2xx response body. It copes
// with a body that is not problem+json (a proxy error page, say) by keeping
// the status and a short excerpt. Control characters are stripped from
// everything taken from the body: the detail is printed to a terminal.
func ErrorFromResponse(status int, body []byte) *Error {
	var p Problem
	if err := json.Unmarshal(body, &p); err == nil && p.Type != "" {
		t := p.Type
		if i := strings.LastIndex(t, "/"); i >= 0 {
			t = t[i+1:]
		}
		return &Error{Status: status, Type: termsafe.String(t), Detail: termsafe.String(p.Detail)}
	}
	excerpt := termsafe.String(strings.TrimSpace(string(body)))
	if len(excerpt) > 160 {
		excerpt = excerpt[:160] + "…"
	}
	detail := fmt.Sprintf("the platform returned HTTP %d", status)
	if excerpt != "" && !strings.HasPrefix(excerpt, "<") {
		detail += ": " + excerpt
	}
	return &Error{Status: status, Type: typeForStatus(status), Detail: detail}
}

func typeForStatus(status int) string {
	switch status {
	case 401:
		return "unauthenticated"
	case 403:
		return "forbidden"
	case 404:
		return "not-found"
	case 400, 422:
		return "validation"
	case 409:
		return "conflict"
	case 429:
		return "rate-limited"
	}
	return "internal"
}

// DefaultMaxResponseBytes caps any single response body the API client reads.
// The generated *WithResponse methods io.ReadAll the body; without a cap a
// hostile or broken endpoint could make the CLI allocate without bound.
const DefaultMaxResponseBytes = 32 << 20

// Options for New.
type Options struct {
	BaseURL   string
	Token     string
	UserAgent string
	// Timeout bounds a whole ordinary request, body included. Streaming
	// requests (log follow) are exempt from that; they are bounded by the
	// time to response headers and by the caller's context instead.
	Timeout time.Duration
	// MaxResponseBytes caps a response body; 0 means DefaultMaxResponseBytes.
	MaxResponseBytes int64
}

// New builds a typed client that authenticates every request.
//
// It also refuses three things the generated client would otherwise do
// quietly: follow a redirect (a 308 turns DELETE …/vms/web/ssh-keys/.. into
// DELETE …/vms/web), send a request whose path shows a collapsed or empty
// segment, and read an unbounded body.
func New(o Options) (*ClientWithResponses, error) {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	limit := o.MaxResponseBytes
	if limit <= 0 {
		limit = DefaultMaxResponseBytes
	}
	editor := func(_ context.Context, req *http.Request) error {
		if err := CheckRequestPath(req.URL); err != nil {
			return err
		}
		if o.Token != "" {
			req.Header.Set("Authorization", "Bearer "+o.Token)
		}
		if o.UserAgent != "" {
			req.Header.Set("User-Agent", o.UserAgent)
		}
		req.Header.Set("Accept", "application/json, application/problem+json")
		return nil
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = timeout
	d := &doer{
		std: &http.Client{
			Timeout:       timeout,
			Transport:     &limitedTransport{base: base, limit: limit},
			CheckRedirect: NoRedirects,
		},
		// No overall Timeout: http.Client.Timeout covers reading the body,
		// which would cut `app logs -f` off mid-stream. The header timeout on
		// the transport and the caller's context still bound it.
		stream: &http.Client{Transport: base, CheckRedirect: NoRedirects},
	}
	return NewClientWithResponses(o.BaseURL, WithHTTPClient(d), WithRequestEditorFn(editor))
}

// NoRedirects is an http.Client CheckRedirect that never follows one. The
// 3xx is handed back to the caller, which reports it as a RedirectError.
func NoRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// RedirectError is a 3xx from the platform, which the CLI refuses to follow:
// Go would replay a DELETE or POST against wherever the Location points, and
// the API never legitimately redirects.
type RedirectError struct {
	Method   string
	URL      string
	Status   int
	Location string
}

func (e *RedirectError) Error() string {
	loc := e.Location
	if loc == "" {
		loc = "(no Location header)"
	}
	return fmt.Sprintf("the platform answered %s %s with a redirect (HTTP %d) to %s; the CLI does not follow redirects — check the API URL",
		e.Method, termsafe.String(e.URL), e.Status, termsafe.String(loc))
}

// AsRedirectError turns a 3xx response into a *RedirectError, closing its
// body; any other response gives nil.
func AsRedirectError(req *http.Request, res *http.Response) error {
	if res == nil || res.StatusCode < 300 || res.StatusCode > 399 {
		return nil
	}
	_ = res.Body.Close()
	e := &RedirectError{Status: res.StatusCode, Location: res.Header.Get("Location")}
	if req != nil {
		e.Method = req.Method
		if req.URL != nil {
			e.URL = req.URL.Redacted()
		}
	}
	return e
}

// doer sends ordinary requests through a client with an overall timeout and a
// body cap, and streaming requests through one without either.
type doer struct{ std, stream *http.Client }

func (d *doer) Do(req *http.Request) (*http.Response, error) {
	c := d.std
	if isStreaming(req) {
		c = d.stream
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if rerr := AsRedirectError(req, res); rerr != nil {
		return nil, rerr
	}
	return res, nil
}

// isStreaming reports whether req is a long-lived stream: today only
// GET …/logs?follow=true.
func isStreaming(req *http.Request) bool {
	return req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/logs") &&
		req.URL.Query().Get("follow") == "true"
}

// PathError is a request the client refused to send because its path shows a
// path argument collapsed into its neighbours.
type PathError struct{ Path string }

func (e *PathError) Error() string {
	return fmt.Sprintf("refusing to send a request to %s: a name in it is empty, \".\" or \"..\"", termsafe.String(e.Path))
}

// CheckRequestPath refuses a request URL whose path ends in "/", contains
// "//", or still holds a "." or ".." segment. The generated client
// path-escapes each argument (which leaves dots alone) and then resolves the
// operation path against the server URL, which removes dot segments — so
// `ssh-key remove web ..` becomes DELETE …/vms/web/. No generated operation
// path ends in "/" or has an empty segment, so any of these means an argument
// was empty, "." or "..". This is the second line of defence; the command
// layer rejects such arguments before a request is built.
func CheckRequestPath(u *url.URL) error {
	p := u.EscapedPath()
	if p == "" || p == "/" {
		return nil
	}
	if strings.HasSuffix(p, "/") || strings.Contains(p, "//") {
		return &PathError{Path: p}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return &PathError{Path: p}
		}
	}
	return nil
}

// limitedTransport caps every response body at limit bytes. Reading past the
// cap fails rather than truncating silently, so a decoder never sees a body
// cut short and mistakes it for a whole one.
type limitedTransport struct {
	base  http.RoundTripper
	limit int64
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil || res == nil || res.Body == nil {
		return res, err
	}
	res.Body = &limitedBody{rc: res.Body, left: t.limit, limit: t.limit}
	return res, nil
}

// ErrResponseTooLarge is returned when a response body exceeds the cap.
var ErrResponseTooLarge = errors.New("response from the platform is too large")

type limitedBody struct {
	rc    io.ReadCloser
	left  int64
	limit int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One more byte decides whether the body ended exactly at the cap.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			return 0, fmt.Errorf("%w (over %d bytes)", ErrResponseTooLarge, b.limit)
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// Check returns nil for a 2xx response, else the platform's problem as *Error.
// Every command calls this on the raw response before reading the typed body.
func Check(res *http.Response, body []byte) error {
	if res == nil {
		return errors.New("no response")
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	return ErrorFromResponse(res.StatusCode, body)
}
