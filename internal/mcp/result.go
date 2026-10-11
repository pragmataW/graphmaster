package mcp

import (
	"errors"
	"fmt"
	"strings"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pragmataW/graphmaster/internal/index"
)

// safeCall runs fn and converts its text, error or panic into a tool
// result. Errors are reported to the model, never to the transport, so a
// failing tool cannot end the session.
func safeCall(tool string, fn func() (string, error)) (res *gomcp.CallToolResult) {
	defer func() {
		if r := recover(); r != nil {
			res = errorResult(tool, fmt.Errorf("internal error: %v", r))
		}
	}()
	text, err := fn()
	if err != nil {
		return errorResult(tool, err)
	}
	return textResult(text, false)
}

// errorResult renders err in the banner format of the tools.
func errorResult(tool string, err error) *gomcp.CallToolResult {
	// query errors start with the tool name, which the banner already has.
	msg := strings.TrimPrefix(err.Error(), tool+": ")
	if errors.Is(err, index.ErrNotGitRepo) && !strings.Contains(msg, "index --force") {
		msg = index.NotGitHint + " (" + msg + ")"
	}
	return textResult("graphmaster "+tool+" · error: "+msg+"\n", true)
}

func textResult(text string, isError bool) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{&gomcp.TextContent{Text: text}},
		IsError: isError,
	}
}
