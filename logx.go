package logx

import (
	"io"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/pkgerrors"
	"gopkg.in/natefinch/lumberjack.v2"
)

// packageFieldName is the log field carrying the caller's Go import path.
const packageFieldName = "package"

// defaultCallerSegments is how many trailing path segments the caller field keeps
// when CallerFieldLength is 0 (e.g. "pkg/sub/file.go:42"). Three disambiguates
// files with the same name in different packages.
const defaultCallerSegments = 3

var (
	logger    zerolog.Logger
	loggerMux sync.RWMutex // protects logger re-initialization
	startTime time.Time
	pid       = os.Getpid()

	// slogLogger mirrors logger but never carries the caller hook. The slog
	// bridge (NewSlogHandler) uses it and derives caller/package from the
	// record's PC instead, because a hook would resolve every slog line to the
	// handler's own Msg call site rather than the real caller.
	slogLogger zerolog.Logger
	// baseWriter is retained so both loggers can be rebuilt when the global
	// context fields change (SetGlobalContext).
	baseWriter io.Writer
	// globalCtx applies persistent fields to both loggers (e.g. build metadata).
	globalCtx func(zerolog.Context) zerolog.Context
	// includeCaller/includePackage/callerSegments mirror the config so the direct
	// logger's hook and the slog bridge can attach caller and package fields.
	includeCaller  bool
	includePackage bool
	callerSegments int
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
	// IncludeCaller annotates each log line with a "caller" field: the source
	// file and line, truncated to the last CallerFieldLength path segments
	// (e.g. "pkg/sub/file.go:42").
	IncludeCaller bool
	// IncludePackage annotates each log line with a "package" field: the caller's
	// full Go import path (e.g. "github.com/org/repo/internal/controller"). It is
	// independent of IncludeCaller and useful for filtering logs by origin.
	IncludePackage bool
	// CallerFieldLength is how many trailing path segments the "caller" field
	// keeps. 0 uses the default of 3 (e.g. "pkg/sub/file.go:42"); 1 is the lowest
	// explicit value and keeps just the file name. Ignored unless IncludeCaller
	// is set.
	CallerFieldLength int
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
	includePackage = cfg.IncludePackage
	callerSegments = cfg.CallerFieldLength
	rebuildLoggersLocked()

	return nil
}

// rebuildLoggersLocked (re)builds both the direct logger and the slog logger
// from baseWriter, applying globalCtx to each. The direct logger carries logx's
// caller hook when caller/package fields are enabled; the slog logger never does
// (the slog bridge derives them from the record PC). Callers must hold loggerMux.
func rebuildLoggersLocked() {
	base := zerolog.New(baseWriter).With().
		Timestamp().
		Int("pid", pid)
	if globalCtx != nil {
		base = globalCtx(base)
	}
	slogLogger = base.Logger()
	direct := base.Logger()
	if includeCaller || includePackage {
		direct = direct.Hook(callerHook{caller: includeCaller, pkg: includePackage, segs: callerSegments})
	}
	logger = direct
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

// callerConfig returns the caller/package field settings under the read lock,
// for the slog bridge (which sets these fields from the record PC).
func callerConfig() (caller, pkg bool, segs int) {
	loggerMux.RLock()
	defer loggerMux.RUnlock()
	return includeCaller, includePackage, callerSegments
}

// slogBase returns a copy of the caller-hook-free slog logger.
func slogBase() *zerolog.Logger {
	loggerMux.RLock()
	c := slogLogger
	loggerMux.RUnlock()
	return &c
}

// callerHook attaches the caller and/or package field to each direct-logger
// event, resolved from the real call site (see callerFrame).
type callerHook struct {
	caller bool
	pkg    bool
	segs   int
}

func (h callerHook) Run(e *zerolog.Event, _ zerolog.Level, _ string) {
	f := callerFrame()
	if f.File == "" {
		return
	}
	if h.caller {
		e.Str(zerolog.CallerFieldName, trimFile(f.File, h.segs)+":"+strconv.Itoa(f.Line))
	}
	if h.pkg {
		e.Str(packageFieldName, resolvePkg(f.Function))
	}
}

// callerFrame returns the first stack frame outside runtime, zerolog, and logx —
// i.e. the code that invoked the logger. Walking by package prefix (rather than a
// fixed skip count) keeps it correct regardless of zerolog's internal call depth.
func callerFrame() runtime.Frame {
	var pcs [32]uintptr
	n := runtime.Callers(0, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.Function != "" && !isInternalFrame(f.Function) {
			return f
		}
		if !more {
			return runtime.Frame{}
		}
	}
}

// isInternalFrame reports whether fn belongs to the runtime, zerolog, or logx
// itself. The "." / "/" boundaries matter: they exclude these packages and their
// subpackages without also matching a consumer package that merely shares the
// prefix (e.g. the external logx_test package or a "logxfoo" module).
func isInternalFrame(fn string) bool {
	for _, p := range []string{"runtime", "github.com/rs/zerolog", "github.com/automa-saga/logx"} {
		if fn == p || strings.HasPrefix(fn, p+".") || strings.HasPrefix(fn, p+"/") {
			return true
		}
	}
	return false
}

// trimFile keeps the last n '/'-separated segments of file (n<=0 uses the
// default), so log lines trace to source without full absolute paths.
func trimFile(file string, n int) string {
	if n <= 0 {
		n = defaultCallerSegments
	}
	segments := 0
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '/' {
			segments++
			if segments == n {
				return file[i+1:]
			}
		}
	}
	return file
}

// mainPkg is the import path of the binary's main package, resolved once from
// build info. The Go runtime names main-package functions "main.<func>" with no
// import path, so resolvePkg substitutes this. Empty when unavailable (e.g.
// `go run <file.go>`, which builds as "command-line-arguments").
var mainPkg = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Path != "" && bi.Path != "command-line-arguments" {
		return bi.Path
	}
	return ""
}()

// resolvePkg returns pkgOf(fn), substituting the real main package path for the
// runtime's bare "main" when it is known.
func resolvePkg(fn string) string {
	if p := pkgOf(fn); p != "main" || mainPkg == "" {
		return p
	}
	return mainPkg
}

// pkgOf extracts the Go import path from a runtime function name, e.g.
// "github.com/org/repo/pkg.(*T).Method" → "github.com/org/repo/pkg".
func pkgOf(fn string) string {
	if fn == "" {
		return ""
	}
	slash := strings.LastIndexByte(fn, '/')
	dot := strings.IndexByte(fn[slash+1:], '.')
	if dot < 0 {
		return fn
	}
	return fn[:slash+1+dot]
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
