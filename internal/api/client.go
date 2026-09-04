package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

/*
Hand-written companions to the generated client (gen.go).

The generated code knows the wire; this file knows the CLI: it attaches the
credential and User-Agent, and turns the platform's RFC 9457 problem responses
into a typed error the commands can map to an exit code and a sentence.
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
		return e.Detail
	}
	return fmt.Sprintf("the platform returned HTTP %d", e.Status)
}

// Is lets callers match on type: errors.Is(err, api.ErrNotFound).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Type == e.Type
}

var (
	ErrUnauthenticated = &Error{Type: "unauthenticated"}
	ErrForbidden       = &Error{Type: "forbidden"}
	ErrNotFound        = &Error{Type: "not-found"}
	ErrValidation      = &Error{Type: "validation"}
	ErrConflict        = &Error{Type: "conflict"}
)

// ErrorFromResponse builds an *Error from a non-2xx response body. It copes
// with a body that is not problem+json (a proxy error page, say) by keeping
// the status and a short excerpt.
func ErrorFromResponse(status int, body []byte) *Error {
	var p Problem
	if err := json.Unmarshal(body, &p); err == nil && p.Type != "" {
		t := p.Type
		if i := strings.LastIndex(t, "/"); i >= 0 {
			t = t[i+1:]
		}
		return &Error{Status: status, Type: t, Detail: p.Detail}
	}
	excerpt := strings.TrimSpace(string(body))
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

// Options for New.
type Options struct {
	BaseURL   string
	Token     string
	UserAgent string
	Timeout   time.Duration
}

// New builds a typed client that authenticates every request.
func New(o Options) (*ClientWithResponses, error) {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	editor := func(_ context.Context, req *http.Request) error {
		if o.Token != "" {
			req.Header.Set("Authorization", "Bearer "+o.Token)
		}
		if o.UserAgent != "" {
			req.Header.Set("User-Agent", o.UserAgent)
		}
		req.Header.Set("Accept", "application/json, application/problem+json")
		return nil
	}
	return NewClientWithResponses(o.BaseURL, WithHTTPClient(&http.Client{Timeout: timeout}), WithRequestEditorFn(editor))
}

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
