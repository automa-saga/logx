package logx

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitialize_FileLogging(t *testing.T) {
	tempDir := t.TempDir()
	logFile := "test.log"

	err := Initialize(LoggingConfig{
		Level:       "info",
		FileLogging: true,
		Directory:   tempDir,
		Filename:    logFile,
		MaxSize:     1,
		MaxBackups:  1,
		MaxAge:      1,
		Compress:    false,
	})
	assert.NoError(t, err)

	logger := As()
	assert.NotNil(t, logger)
	logger.Info().Msg("Test info message")

	// Verify log file exists
	logFilePath := filepath.Join(tempDir, logFile)
	_, err = os.Stat(logFilePath)
	assert.NoError(t, err)
}

func TestInitialize_TimeFormat(t *testing.T) {
	tempDir := t.TempDir()
	logFile := "test.log"
	const layout = "2006-01-02T15:04:05.000Z07:00"

	err := Initialize(LoggingConfig{
		Level:       "info",
		FileLogging: true,
		Directory:   tempDir,
		Filename:    logFile,
		MaxSize:     1,
		MaxBackups:  1,
		MaxAge:      1,
		TimeFormat:  layout,
	})
	assert.NoError(t, err)
	t.Cleanup(func() {
		_ = Initialize(LoggingConfig{Level: "info", ConsoleLogging: true})
	})

	As().Info().Msg("Test info message")

	content, err := os.ReadFile(filepath.Join(tempDir, logFile))
	assert.NoError(t, err)

	m := decode(t, bytes.NewBuffer(content))
	_, err = time.Parse(layout, m["time"].(string))
	assert.NoError(t, err)
}

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// was written. It restores the original os.Stdout before returning.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestInitialize_ConsoleLogging_JSON(t *testing.T) {
	t.Cleanup(func() {
		_ = Initialize(LoggingConfig{Level: "info", ConsoleLogging: true})
	})

	out := captureStdout(t, func() {
		err := Initialize(LoggingConfig{Level: "info", ConsoleLogging: false})
		require.NoError(t, err)
		As().Info().Msg("structured output")
	})

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m),
		"ConsoleLogging:false should emit structured JSON, got %q", out)
	assert.Equal(t, "structured output", m["message"])
	assert.Equal(t, "info", m["level"])
}

func TestInitialize_ConsoleLogging_Human(t *testing.T) {
	t.Cleanup(func() {
		_ = Initialize(LoggingConfig{Level: "info", ConsoleLogging: true})
	})

	out := captureStdout(t, func() {
		err := Initialize(LoggingConfig{Level: "info", ConsoleLogging: true})
		require.NoError(t, err)
		As().Info().Msg("human output")
	})

	// The console writer emits human-readable output, not parseable JSON.
	var m map[string]any
	assert.Error(t, json.Unmarshal([]byte(out), &m),
		"ConsoleLogging:true should emit human-readable output, got %q", out)
	assert.Contains(t, out, "human output")
}

func TestInitialize_UTC(t *testing.T) {
	tempDir := t.TempDir()
	logFile := "test.log"
	t.Cleanup(func() {
		_ = Initialize(LoggingConfig{Level: "info", ConsoleLogging: true})
	})

	err := Initialize(LoggingConfig{
		Level:       "info",
		FileLogging: true,
		Directory:   tempDir,
		Filename:    logFile,
		UTC:         true,
	})
	require.NoError(t, err)

	As().Info().Msg("utc message")

	content, err := os.ReadFile(filepath.Join(tempDir, logFile))
	require.NoError(t, err)

	m := decode(t, bytes.NewBuffer(content))
	ts, err := time.Parse(time.RFC3339, m["time"].(string))
	require.NoError(t, err)
	_, offset := ts.Zone()
	assert.Equal(t, 0, offset, "timestamp should be in UTC")
}

func TestInitialize_IncludeCaller(t *testing.T) {
	t.Cleanup(func() {
		_ = Initialize(LoggingConfig{Level: "info", ConsoleLogging: true})
	})

	out := captureStdout(t, func() {
		err := Initialize(LoggingConfig{
			Level:          "info",
			ConsoleLogging: false,
			IncludeCaller:  true,
		})
		require.NoError(t, err)
		As().Info().Msg("caller message")
	})

	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	caller, ok := m["caller"].(string)
	require.True(t, ok, "expected a caller field, got %v", m)
	assert.Contains(t, caller, "logx_test.go:")
	assert.False(t, filepath.IsAbs(caller), "caller should be truncated, got %q", caller)
}

// TestInitialize_IncludeCaller_RestoresDefault verifies that disabling
// IncludeCaller restores zerolog's default CallerMarshalFunc instead of leaking
// shortCaller into the process-global state.
func TestInitialize_IncludeCaller_RestoresDefault(t *testing.T) {
	t.Cleanup(func() {
		zerolog.CallerMarshalFunc = defaultCallerMarshalFunc
		_ = Initialize(LoggingConfig{Level: "info", ConsoleLogging: true})
	})

	// Enable, then disable.
	require.NoError(t, Initialize(LoggingConfig{Level: "info", IncludeCaller: true}))
	require.NoError(t, Initialize(LoggingConfig{Level: "info", IncludeCaller: false}))

	// A logger that opts into the caller directly should now use the default
	// (untruncated, absolute) marshaler rather than shortCaller.
	var buf bytes.Buffer
	l := zerolog.New(&buf).With().Caller().Logger()
	l.Info().Msg("check")

	m := decode(t, &buf)
	caller, ok := m["caller"].(string)
	require.True(t, ok, "expected a caller field, got %v", m)
	assert.True(t, filepath.IsAbs(caller),
		"default marshaler should emit the absolute path, got %q", caller)
}

func TestShortCaller(t *testing.T) {
	cases := []struct {
		file string
		line int
		want string
	}{
		{"/home/user/project/pkg/sub/file.go", 42, "pkg/sub/file.go:42"},
		{"pkg/sub/file.go", 7, "pkg/sub/file.go:7"},
		{"file.go", 1, "file.go:1"},
		{"a/b.go", 3, "a/b.go:3"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, shortCaller(0, c.file, c.line))
	}
}

func TestInitialize_InvalidLogLevel(t *testing.T) {
	err := Initialize(LoggingConfig{
		Level:          "invalid",
		ConsoleLogging: true,
	})
	assert.Error(t, err)
}

func TestExecutionTime(t *testing.T) {
	StartTimer()
	assert.NotEmpty(t, ExecutionTime())
}

func TestGetPid(t *testing.T) {
	pid := GetPid()
	assert.Equal(t, os.Getpid(), pid)
}

func TestSetLogger(t *testing.T) {
	var buf bytes.Buffer
	custom := zerolog.New(&buf).With().Str("custom", "true").Logger()

	prev := *As()
	SetLogger(custom)
	t.Cleanup(func() {
		SetLogger(prev)
	})

	As().Info().Msg("hello")
	assert.Contains(t, buf.String(), "hello")
	assert.Contains(t, buf.String(), `"custom":"true"`)
}

// TestAs_ReturnsPointerToCopy verifies that As() returns a pointer and that
// each call returns an independent copy (not sharing the same pointer)
func TestAs_ReturnsPointerToCopy(t *testing.T) {
	logger1 := As()
	logger2 := As()

	// Verify both are non-nil pointers
	assert.NotNil(t, logger1)
	assert.NotNil(t, logger2)

	// Verify they are different pointers (different copies)
	// This ensures no shared mutable state
	assert.NotSame(t, logger1, logger2, "Each As() call should return a different pointer")

	// Verify both loggers work correctly
	logger1.Info().Msg("Test from logger1")
	logger2.Debug().Msg("Test from logger2")
}

// BenchmarkAs measures the performance of As() returning a pointer to a copy
func BenchmarkAs(b *testing.B) {
	// Initialize logger once
	err := Initialize(LoggingConfig{
		Level:          "info",
		ConsoleLogging: false, // Disable output for clean benchmark
	})
	if err != nil {
		b.Fatalf("Failed to initialize logger: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = As()
	}
}

// BenchmarkAs_WithLogging measures the overhead including actual log call
func BenchmarkAs_WithLogging(b *testing.B) {
	// Initialize logger once
	err := Initialize(LoggingConfig{
		Level:          "info",
		ConsoleLogging: false, // Disable output for clean benchmark
	})
	if err != nil {
		b.Fatalf("Failed to initialize logger: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		As().Info().Msg("benchmark message")
	}
}

// BenchmarkAs_PerIteration measures the cost of calling As() inside a hot loop.
// This models the "Standard Usage" pattern from the As() docs and is expected
// to allocate one logger copy (~100 bytes) per iteration.
func BenchmarkAs_PerIteration(b *testing.B) {
	err := Initialize(LoggingConfig{
		Level:          "info",
		ConsoleLogging: false,
	})
	if err != nil {
		b.Fatalf("Failed to initialize logger: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Call As() on every iteration (the pattern to avoid in hot paths).
		As().Debug().Int("i", i).Msg("processing")
	}
}

// BenchmarkAs_StoredOnce measures the cost when the logger is stored once
// before the loop. This models the "High-Frequency Logging" pattern from the
// As() docs and is expected to allocate 0 bytes per iteration.
func BenchmarkAs_StoredOnce(b *testing.B) {
	err := Initialize(LoggingConfig{
		Level:          "info",
		ConsoleLogging: false,
	})
	if err != nil {
		b.Fatalf("Failed to initialize logger: %v", err)
	}

	logger := As() // Allocate once, outside the hot loop.

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Debug().Int("i", i).Msg("processing")
	}
}

// BenchmarkAs_Enabled_PerIteration measures the per-iteration pattern when the
// log event is actually emitted (level enabled), so the cost includes both the
// As() copy and zerolog's event encoding/write. Output goes to io.Discard to
// exclude real I/O while still exercising the encoder.
func BenchmarkAs_Enabled_PerIteration(b *testing.B) {
	SetLogger(zerolog.New(io.Discard).Level(zerolog.InfoLevel))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		As().Info().Int("i", i).Msg("processing")
	}
}

// BenchmarkAs_Enabled_StoredOnce measures the stored-once pattern with the log
// event emitted. The difference versus BenchmarkAs_Enabled_PerIteration isolates
// the cost saved by hoisting As() out of the loop.
func BenchmarkAs_Enabled_StoredOnce(b *testing.B) {
	SetLogger(zerolog.New(io.Discard).Level(zerolog.InfoLevel))

	logger := As() // Allocate once, outside the hot loop.

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info().Int("i", i).Msg("processing")
	}
}

// BenchmarkAs_Concurrent measures performance under concurrent access
func BenchmarkAs_Concurrent(b *testing.B) {
	// Initialize logger once
	err := Initialize(LoggingConfig{
		Level:          "info",
		ConsoleLogging: false,
	})
	if err != nil {
		b.Fatalf("Failed to initialize logger: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			As().Info().Msg("concurrent benchmark")
		}
	})
}

// TestAs_ThreadSafety verifies that concurrent calls to As() are safe
func TestAs_ThreadSafety(t *testing.T) {
	// Initialize logger
	err := Initialize(LoggingConfig{
		Level:          "info",
		ConsoleLogging: false,
	})
	assert.NoError(t, err)

	// Spawn multiple goroutines calling As() concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				logger := As()
				logger.Info().Int("goroutine", id).Int("iteration", j).Msg("concurrent test")
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// If we get here without race detector complaints, the test passes
	assert.True(t, true, "Concurrent access to As() should be thread-safe")
}
