// Package unfirehose wraps the unfirehose Go SDK for orchestra's use.
// The core logger comes from github.com/russellballestrini/unfirehose-sdks/go.
// This package sets the harness to "orchestra" and provides the same API.
package unfirehose

import (
	uf "github.com/russellballestrini/unfirehose-sdks/go"
)

const Harness = "orchestra"

// Re-export all types from the SDK.
type ContentBlock = uf.ContentBlock
type Usage = uf.Usage
type InputTokenDetail = uf.InputTokenDetail
type OutputTokenDetail = uf.OutputTokenDetail
type TodoObject = uf.TodoObject
type Logger = uf.Logger

// Re-export content block builders.
var (
	TextBlock      = uf.TextBlock
	ReasoningBlock = uf.ReasoningBlock
	ToolCallBlock  = uf.ToolCallBlock
	ToolResultBlock = uf.ToolResultBlock
	ImageBlock     = uf.ImageBlock
	FileBlock      = uf.FileBlock
)

// Re-export metric type validation.
var ValidMetricTypes = uf.ValidMetricTypes

// NewLogger creates a logger with harness="orchestra" writing to
// ~/.unfirehose/canonical/orchestra/.
func NewLogger(version string) (*Logger, error) {
	return uf.NewLogger(Harness, version)
}

// NewLoggerWithDir creates a logger writing to a custom directory.
func NewLoggerWithDir(dir, version string) *Logger {
	return uf.NewLoggerWithDir(dir, Harness, version)
}
