package logx

import (
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/pkgerrors"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	logger    zerolog.Logger
	loggerMux sync.RWMutex // protects logger re-initialization
	startTime time.Time
	pid       = os.Getpid()
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

	console := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}

	var writers []io.Writer
	if cfg.FileLogging {
		logFile, err := newRollingFile(cfg)
		if err != nil {
			return err
		}

		fileWriter := zerolog.New(logFile).With().Timestamp().Logger()
		writers = append(writers, console, fileWriter)
	} else {
		writers = append(writers, console)
	}

	mw := zerolog.MultiLevelWriter(writers...)
	logger = zerolog.New(mw).With().
		Timestamp().
		Int("pid", pid).
		Logger()

	return nil
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
//   - Each call: ~10ns CPU + ~100 bytes allocation
//   - Negligible compared to actual log I/O (~1-10ms)
//   - Only significant in loops with >1000 iterations
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

// SetLogger replaces the global logger with a custom-built zerolog.Logger.
// Use this when you need to swap the logger at runtime (e.g., to suppress
// console output for a TUI or attach custom hooks). Safe to call concurrently.
func SetLogger(l zerolog.Logger) {
	loggerMux.Lock()
	logger = l
	loggerMux.Unlock()
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
