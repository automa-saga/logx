package logx_test

// External-package tests (package logx_test) exercise the direct-logger caller
// and package fields the way a real consumer would: from outside logx, so the
// caller-resolution walk does not treat the test's own frames as internal.

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/automa-saga/logx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture swaps os.Stdout for the duration of fn (which must call Initialize so
// the sink binds to the redirected stdout) and returns the first line written.
func capture(t *testing.T, fn func()) map[string]any {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = orig
		_ = logx.Initialize(logx.LoggingConfig{ConsoleLogging: true})
	})
	fn()
	_ = w.Close()
	line, _ := bufio.NewReader(r).ReadString('\n')
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &m), "line=%q", line)
	return m
}

func TestIncludeCaller_External(t *testing.T) {
	m := capture(t, func() {
		require.NoError(t, logx.Initialize(logx.LoggingConfig{
			Level: "info", ConsoleLogging: false, IncludeCaller: true,
		}))
		logx.As().Info().Msg("caller message")
	})
	caller, ok := m["caller"].(string)
	require.True(t, ok, "expected a caller field, got %v", m)
	assert.Contains(t, caller, "logx_caller_ext_test.go:", "caller should be the call site, got %q", caller)
	assert.False(t, strings.HasPrefix(caller, "/"), "caller should be truncated, got %q", caller)
	_, hasPkg := m["package"]
	assert.False(t, hasPkg, "package must be independent of caller, got %v", m)
}

func TestIncludePackage_External(t *testing.T) {
	m := capture(t, func() {
		require.NoError(t, logx.Initialize(logx.LoggingConfig{
			Level: "info", ConsoleLogging: false, IncludePackage: true,
		}))
		logx.As().Info().Msg("package message")
	})
	pkg, ok := m["package"].(string)
	require.True(t, ok, "expected a package field, got %v", m)
	assert.Equal(t, "github.com/automa-saga/logx_test", pkg)
	_, hasCaller := m["caller"]
	assert.False(t, hasCaller, "caller must be independent of package, got %v", m)
}

func TestCallerFieldLength_External(t *testing.T) {
	m := capture(t, func() {
		require.NoError(t, logx.Initialize(logx.LoggingConfig{
			Level: "info", ConsoleLogging: false, IncludeCaller: true, CallerFieldLength: 1,
		}))
		logx.As().Info().Msg("one segment")
	})
	caller, _ := m["caller"].(string)
	assert.False(t, strings.Contains(strings.TrimSuffix(caller, caller[strings.IndexByte(caller, ':'):]), "/"),
		"expected a single path segment, got %q", caller)
	assert.True(t, strings.HasPrefix(caller, "logx_caller_ext_test.go:"), "got %q", caller)
}

func TestCallerAndPackage_External(t *testing.T) {
	m := capture(t, func() {
		require.NoError(t, logx.Initialize(logx.LoggingConfig{
			Level: "info", ConsoleLogging: false, IncludeCaller: true, IncludePackage: true,
		}))
		logx.As().Info().Msg("both")
	})
	caller, _ := m["caller"].(string)
	pkg, _ := m["package"].(string)
	assert.Contains(t, caller, "logx_caller_ext_test.go:")
	assert.Equal(t, "github.com/automa-saga/logx_test", pkg)
}

func TestSlog_CallerAndPackage_External(t *testing.T) {
	m := capture(t, func() {
		require.NoError(t, logx.Initialize(logx.LoggingConfig{
			Level: "info", ConsoleLogging: false, IncludeCaller: true, IncludePackage: true,
		}))
		slog.New(logx.NewSlogHandler()).Info("via slog")
	})
	caller, _ := m["caller"].(string)
	pkg, _ := m["package"].(string)
	assert.Contains(t, caller, "logx_caller_ext_test.go:")
	assert.Equal(t, "github.com/automa-saga/logx_test", pkg)
}
