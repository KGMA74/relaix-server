package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is a tool an embedding program adds to the gateway's own: the cloud's
// administration, say. Like the built-in ones it acts through the API, with
// the caller's key.
type Tool struct {
	Name        string
	Title       string
	Description string
	// Input is the arguments' JSON Schema.
	Input       map[string]any
	ReadOnly    bool
	Destructive bool
	// AdminOnly lists and runs the tool only for a key that sees every
	// account: the platform administrator's.
	AdminOnly bool
	Call      func(ctx context.Context, api Caller, args json.RawMessage) (any, error)
}

// Caller makes requests to the API with the caller's key.
type Caller interface {
	// Do sends a request and decodes a 2xx answer into out (when not nil).
	// Any other answer is an error whose message the API gave, fit for the
	// assistant to read.
	Do(ctx context.Context, method, path string, body, out any) error
}

// Do implements Caller.
func (c *caller) Do(ctx context.Context, method, path string, body, out any) error {
	return c.do(ctx, method, path, body, out)
}

// Errorf is a failure caused by the request, shown to the assistant as is.
func Errorf(format string, args ...any) error { return userError{fmt.Sprintf(format, args...)} }

// Decode reads a tool's arguments strictly: a misspelt field is reported.
func Decode(args json.RawMessage, v any) error { return decode(args, v) }

func (t Tool) definition() map[string]any {
	return tool(t.Name, t.Title, t.Description, t.Input, t.ReadOnly, t.Destructive)
}

// visible reports whether c may see and run t.
func (t Tool) visible(c *caller) bool { return !t.AdminOnly || c.principal.AllAccounts }
