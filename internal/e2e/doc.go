// Package e2e holds end-to-end tests that drive a freshly built graphmaster
// binary the way agents do: install/doctor/uninstall against a temporary
// HOME, the MCP server over stdio, hook invocations with synthetic agent
// payloads, and the UI HTTP server. It has no non-test code.
//
// The tests are skipped with -short.
package e2e
