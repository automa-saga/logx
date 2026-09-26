package logx

import (
	"io"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/pkgerrors"
	"gopkg.in/natefinch/lumberjack.v2"
)

// defaultCallerMarshalFunc captures zerolog's built-in caller marshaler so it
// can be restored when IncludeCaller is disabled, avoiding leaking shortCaller
// into the process-global state (and other zerolog loggers).
var defaultCallerMarshalFunc = zerolog.CallerMarshalFunc

var (
	logger    zerolog.Logger
	loggerMux sync.RWMutex // protects logger re-initialization
	startTime time.Time
	pid       = os.Getpid()

	// slogLogger mirrors logger but never carries the caller hook. The slog
	// bridge (NewSlogHandler) uses it and derives the caller from the record's
	// PC instead, because the shared hook would resolve every slog line to the
	// handler's own Msg call site rather than the real caller.
	slogLogger zerolog.Logger
	// baseWriter is retained so both loggers can be rebuilt when the global
	// context fields change (SetGlobalContext).
	baseWriter io.Writer
	// globalCtx applies persistent fields to both loggers (e.g. build metadata).
	globalCtx func(zerolog.Context) zerolog.Context
	// includeCaller records whether IncludeCaller was set, so the slog bridge
	// knows to attach a PC-derived caller.
	includeCaller bool
)

// LoggingConfig holds the configuration for logging.
type LoggingConfig struct {
	// Level is the log level to use (e.g., "Info", "Debug").
	Level string
	// ConsoleLogging enables logging to the console.
	ConsoleLogging bool
	// FileLogging enables logging to a file.
	FileLogging bool
	// Directory specifies the directory for log files (used if FileLogging is enabled).
	Directory string
	// Filename is the name of the log file.
	Filename string
	// MaxSize is the maximum size (in MB) of a log file before it is rolled.
	MaxSize int
	// MaxBackups is the maximum number of rolled log files to keep.
	MaxBackups int
	// MaxAge is the maximum age (in days) to keep a log file.
	MaxAge int
	// Compress enables compression of rolled log files.
	Compress bool
	// TimeFormat is the timestamp layout for console and file output. Defaults to time.RFC3339.
	TimeFormat string
	// UTC pins log timestamps to UTC. When false, timestamps use local time.
	UTC bool
	// IncludeCaller annotates each log line with the source file and line
	// (truncated to the last few path segments, e.g. "pkg/sub/file.go:42").
	IncludeCaller bool
}

func init() {
	StartTimer()
	_ = initializeLogger(&LoggingConfig{ConsoleLogging: true})
}

// Initialize configures the logger with custom settings.
// Can be called to reconfigure the logger (e.g., in tests).
func Initialize(cfg LoggingConfig) error {
	loggerMux.Lock()
	defer loggerMux.Unlock()
	return initializeLogger(&cfg)
}

// initializeLogger is the internal initialization function
// Must be called with loggerMux held or via initOnce
func initializeLogger(cfg *LoggingConfig) error {
	l, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		return err
	}
	zerolog.SetGlobalLevel(l)
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack

	timeFormat := cfg.TimeFormat
	if timeFormat == "" {
		timeFormat = time.RFC3339
	}
	zerolog.TimeFieldFormat = timeFormat

	if cfg.UTC {
		// The closure is required: time.Now().UTC is a method value that binds
		// the receiver once, freezing every line to the instant this ran.
		zerolog.TimestampFunc = func() time.Time { return time.Now().UTC() }
	} else {
		zerolog.TimestampFunc = time.Now
	}

	if cfg.IncludeCaller {
		zerolog.CallerMarshalFunc = shortCaller
	} else {
		zerolog.CallerMarshalFunc = defaultCallerMarshalFunc
	}

	// The console sink honors ConsoleLogging: human-readable when true, raw
	// structured JSON when false. The file sink is always JSON.
	var consoleSink io.Writer
	if cfg.ConsoleLogging {
		cw := zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: timeFormat,
		}
		if cfg.UTC {
			// Render the parsed timestamp in UTC too; otherwise the console
			// writer reformats it in local time even when the field is UTC.
			cw.TimeLocation = time.UTC
		}
		consoleSink = cw
	} else {
		consoleSink = os.Stdout
	}

	var writers []io.Writer
	if cfg.FileLogging {
		logFile, err := newRollingFile(cfg)
		if err != nil {
			return err
		}

		fileWriter := zerolog.New(logFile).With().Timestamp().Logger()
		writers = append(writers, consoleSink, fileWriter)
	} else {
		writers = append(writers, consoleSink)
	}

	baseWriter = zerolog.MultiLevelWriter(writers...)
	includeCaller = cfg.IncludeCaller
	rebuildLoggersLocked()

	return nil
}

// rebuildLoggersLocked (re)builds both the direct logger and the slog logger
// from baseWriter, applying globalCtx to each. The direct logger carries the
// caller hook when includeCaller is set; the slog logger never does (the slog
// bridge sets caller from the record PC). Callers must hold loggerMux.
func rebuildLoggersLocked() {
	base := zerolog.New(baseWriter).With().
		Timestamp().
		Int("pid", pid)
	if globalCtx != nil {
		base = globalCtx(base)
	}
	slogLogger = base.Logger()
	direct := base
	if includeCaller {
		direct = base.Caller()
	}
	logger = direct.Logger()
}

// SetGlobalContext registers fields applied to every log line on BOTH the direct
// logger (As()) and the slog bridge (NewSlogHandler). Prefer this over building a
// logger with SetLogger(As().With()...) when adding persistent fields such as
// build metadata: fields added that way only reach As() callers, not logs routed
// through the slog handler. Pass nil to clear. Safe to call concurrently.
func SetGlobalContext(apply func(zerolog.Context) zerolog.Context) {
	loggerMux.Lock()
	defer loggerMux.Unlock()
	globalCtx = apply
	rebuildLoggersLocked()
}

// callerEnabled reports whether IncludeCaller is active, under the read lock.
func callerEnabled() bool {
	loggerMux.RLock()
	defer loggerMux.RUnlock()
	return includeCaller
}

// slogBase returns a copy of the caller-hook-free slog logger.
func slogBase() *zerolog.Logger {
	loggerMux.RLock()
	c := slogLogger
	loggerMux.RUnlock()
	return &c
}

// shortCaller renders a caller as the last few path segments plus the line
// number (e.g. "pkg/sub/file.go:42"), so log lines can be traced to source
// without emitting full absolute paths.
func shortCaller(_ uintptr, file string, line int) string {
	const maxSegments = 3

	short := file
	segments := 0
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '/' {
			segments++
			if segments == maxSegments {
				short = file[i+1:]
				break
			}
		}
	}

	return short + ":" + strconv.Itoa(line)
}

// As returns a pointer to a shallow copy of the global logger.
//
// USAGE GUIDELINES:
//
// Standard Usage (Low-Frequency Logging):
// For most logging scenarios (< 1000 calls/sec), call As() directly:
//
//	logx.As().Info().Msg("processing request")
//	logx.As().Debug().Str("file", name).Msg("processing file")
//
// This includes:
//   - HTTP request handlers
//   - Initialization and setup code
//   - Error handling paths
//   - One-off operations
//
// High-Frequency Logging (Hot Paths):
// For tight loops or high-frequency operations (> 1000 calls/sec),
// store the logger once to avoid repeated allocations:
//
//	logger := logx.As()  // Allocate once (~100 bytes)
//	for _, record := range records {
//	    logger.Debug().Str("id", record.ID).Msg("processing")  // 0 bytes per iteration
//	}
//
// This applies to:
//   - Loops processing > 1000 iterations
//   - Stream processing functions
//   - File processing with many small files
//   - Performance-critical hot paths
//
// IMPLEMENTATION DETAILS:
//
// Thread-Safety:
//   - Uses RWMutex to prevent races during logger reconfiguration
//   - Safe to call concurrently from multiple goroutines
//   - Read lock allows concurrent calls to As()
//
// Memory Behavior:
//   - Creates a shallow copy of the logger struct (~100 bytes)
//   - Underlying writer, hooks, and context are shared (pointers)
//   - All returned loggers write to the same destination
//   - Copy is heap-allocated when pointer escapes
//
// Performance:
//   - Each call: ~30ns CPU + 112 bytes / 1 allocation (Apple M1 Max)
//   - Negligible compared to actual log I/O (~1-10ms)
//   - Only significant in loops with >1000 iterations
//   - Measured by BenchmarkAs* in logx_test.go; hoisting As() out of a
//     hot loop drops the per-iteration cost to 0 B / 0 allocs. See the
//     Performance section in README.md for the full benchmark table.
//
// Returns:
//   - A pointer to an independent copy of the logger
//   - The copy shares underlying writer (logs go to same destination)
func As() *zerolog.Logger {
	loggerMux.RLock()
	loggerCopy := logger // Create a copy while holding the lock
	loggerMux.RUnlock()

	return &loggerCopy // Return pointer to the copy, not to the shared global
}

// loggerLevel returns the global logger's minimum level without allocating a
// copy. It lets hot-path checks (such as slog's Enabled fast path) read the
// configured level under the read lock without paying the As() copy.
func loggerLevel() zerolog.Level {
	loggerMux.RLock()
	defer loggerMux.RUnlock()
	return logger.GetLevel()
}

// SetLogger replaces the As() logger with a custom-built zerolog.Logger.
// Use this when you need to swap the direct logger at runtime (e.g., to suppress
// console output for a TUI or attach custom hooks). Safe to call concurrently.
//
// Note: This swaps only the As() logger instance. It does NOT:
//   - update process-wide zerolog settings (zerolog.SetGlobalLevel,
//     ErrorStackMarshaler) — use Initialize for that;
//   - affect the slog bridge (NewSlogHandler), which uses a separate
//     caller-hook-free logger.
//
// To add persistent fields (e.g. build metadata) to every line across BOTH the
// As() logger and the slog bridge, prefer SetGlobalContext — building a logger
// with SetLogger(As().With()...) welds the fields (and the caller hook) onto one
// instance that the slog bridge cannot share and that Initialize would discard.
//
// Loggers previously obtained via As() are shallow copies and will continue
// writing to the old destination; re-fetch via As() after SetLogger.
func SetLogger(l zerolog.Logger) {
	loggerMux.Lock()
	defer loggerMux.Unlock()

	logger = l
}

func StartTimer() {
	startTime = time.Now()
}

func ExecutionTime() string {
	return time.Since(startTime).Round(time.Second).String()
}

func GetPid() int {
	return pid
}

func newRollingFile(cfg *LoggingConfig) (io.Writer, error) {
	return &lumberjack.Logger{
		Filename:   path.Join(cfg.Directory, cfg.Filename),
		MaxBackups: cfg.MaxBackups, // files
		MaxSize:    cfg.MaxSize,    // megabytes
		MaxAge:     cfg.MaxAge,     // days
		Compress:   cfg.Compress,
	}, nil
}
