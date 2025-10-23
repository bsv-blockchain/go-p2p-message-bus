package p2p

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultLoggerDebugf(t *testing.T) {
	tests := []struct {
		name           string
		format         string
		args           []any
		expectedPrefix string
		expectedMsg    string
	}{
		{
			name:           "simple debug message",
			format:         "test message",
			args:           nil,
			expectedPrefix: "[DEBUG]",
			expectedMsg:    "test message",
		},
		{
			name:           "debug message with formatting",
			format:         "user %s logged in with ID %d",
			args:           []any{"alice", 42},
			expectedPrefix: "[DEBUG]",
			expectedMsg:    "user alice logged in with ID 42",
		},
		{
			name:           "debug message with multiple args",
			format:         "processing %d items for %s in %v seconds",
			args:           []any{100, "test", 3.14},
			expectedPrefix: "[DEBUG]",
			expectedMsg:    "processing 100 items for test in 3.14 seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture log output
			var buf bytes.Buffer
			log.SetOutput(&buf)
			defer log.SetOutput(nil)

			logger := &DefaultLogger{}
			logger.Debugf(tt.format, tt.args...)

			output := buf.String()
			assert.Contains(t, output, tt.expectedPrefix)
			assert.Contains(t, output, tt.expectedMsg)
		})
	}
}

func TestDefaultLoggerInfof(t *testing.T) {
	tests := []struct {
		name           string
		format         string
		args           []any
		expectedPrefix string
		expectedMsg    string
	}{
		{
			name:           "simple info message",
			format:         "server started",
			args:           nil,
			expectedPrefix: "[INFO]",
			expectedMsg:    "server started",
		},
		{
			name:           "info message with formatting",
			format:         "listening on port %d",
			args:           []any{8080},
			expectedPrefix: "[INFO]",
			expectedMsg:    "listening on port 8080",
		},
		{
			name:           "info message with string formatting",
			format:         "connected to %s at %s",
			args:           []any{"database", "localhost:5432"},
			expectedPrefix: "[INFO]",
			expectedMsg:    "connected to database at localhost:5432",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log.SetOutput(&buf)
			defer log.SetOutput(nil)

			logger := &DefaultLogger{}
			logger.Infof(tt.format, tt.args...)

			output := buf.String()
			assert.Contains(t, output, tt.expectedPrefix)
			assert.Contains(t, output, tt.expectedMsg)
		})
	}
}

func TestDefaultLoggerWarnf(t *testing.T) {
	tests := []struct {
		name           string
		format         string
		args           []any
		expectedPrefix string
		expectedMsg    string
	}{
		{
			name:           "simple warning message",
			format:         "deprecated API used",
			args:           nil,
			expectedPrefix: "[WARN]",
			expectedMsg:    "deprecated API used",
		},
		{
			name:           "warning with error details",
			format:         "retry attempt %d failed: %s",
			args:           []any{3, "connection timeout"},
			expectedPrefix: "[WARN]",
			expectedMsg:    "retry attempt 3 failed: connection timeout",
		},
		{
			name:           "warning with multiple values",
			format:         "cache miss for key %s, loading from %s",
			args:           []any{"user:123", "database"},
			expectedPrefix: "[WARN]",
			expectedMsg:    "cache miss for key user:123, loading from database",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log.SetOutput(&buf)
			defer log.SetOutput(nil)

			logger := &DefaultLogger{}
			logger.Warnf(tt.format, tt.args...)

			output := buf.String()
			assert.Contains(t, output, tt.expectedPrefix)
			assert.Contains(t, output, tt.expectedMsg)
		})
	}
}

func TestDefaultLoggerErrorf(t *testing.T) {
	tests := []struct {
		name           string
		format         string
		args           []any
		expectedPrefix string
		expectedMsg    string
	}{
		{
			name:           "simple error message",
			format:         "connection failed",
			args:           nil,
			expectedPrefix: "[ERROR]",
			expectedMsg:    "connection failed",
		},
		{
			name:           "error with details",
			format:         "failed to process request: %s",
			args:           []any{"invalid input"},
			expectedPrefix: "[ERROR]",
			expectedMsg:    "failed to process request: invalid input",
		},
		{
			name:           "error with multiple fields",
			format:         "database error on table %s: %v (code: %d)",
			args:           []any{"users", "constraint violation", 1062},
			expectedPrefix: "[ERROR]",
			expectedMsg:    "database error on table users: constraint violation (code: 1062)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log.SetOutput(&buf)
			defer log.SetOutput(nil)

			logger := &DefaultLogger{}
			logger.Errorf(tt.format, tt.args...)

			output := buf.String()
			assert.Contains(t, output, tt.expectedPrefix)
			assert.Contains(t, output, tt.expectedMsg)
		})
	}
}

func TestDefaultLoggerAllLevels(t *testing.T) {
	// This test verifies all log levels work together and produce distinct output
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	logger := &DefaultLogger{}

	logger.Debugf("debug %s", "message")
	logger.Infof("info %s", "message")
	logger.Warnf("warn %s", "message")
	logger.Errorf("error %s", "message")

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// Should have exactly 4 log lines
	assert.Len(t, lines, 4)

	// Verify each level appears once
	assert.Contains(t, output, "[DEBUG]")
	assert.Contains(t, output, "[INFO]")
	assert.Contains(t, output, "[WARN]")
	assert.Contains(t, output, "[ERROR]")

	// Verify messages are correct
	assert.Contains(t, output, "debug message")
	assert.Contains(t, output, "info message")
	assert.Contains(t, output, "warn message")
	assert.Contains(t, output, "error message")
}
