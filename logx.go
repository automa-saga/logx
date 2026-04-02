package logx

import (
	"io"
	"log"
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
	loggerMux sync.RWMutex // protects logger reads and writes
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
	err := Initialize(LoggingConfig{ConsoleLogging: true})
	if err != nil {
		log.Fatalf("failed to initialize logging: %v", err)
	}
}

func Initialize(cfg LoggingConfig) error {
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
	l2 := zerolog.New(mw).With().
		Timestamp().
		Int("pid", pid).
		Logger()
	SetLogger(l2)

	return nil
}

// As returns a pointer to a shallow copy of the global logger.
// The copy shares the underlying writer so logs go to the same destination.
// Safe to call concurrently from multiple goroutines.
func As() *zerolog.Logger {
	loggerMux.RLock()
	loggerCopy := logger
	loggerMux.RUnlock()

	return &loggerCopy
}

// SetLogger replaces the global logger. Use this instead of dereferencing As()
// when you need to swap the logger at runtime (e.g., to suppress console
// output for a TUI). Safe to call concurrently.
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

func newRollingFile(cfg LoggingConfig) (io.Writer, error) {
	return &lumberjack.Logger{
		Filename:   path.Join(cfg.Directory, cfg.Filename),
		MaxBackups: cfg.MaxBackups, // files
		MaxSize:    cfg.MaxSize,    // megabytes
		MaxAge:     cfg.MaxAge,     // days
		Compress:   cfg.Compress,
	}, nil
}
