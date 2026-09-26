package main

import (
	"fmt"

	"github.com/automa-saga/logx"
	"github.com/rs/zerolog"
)

func main() {
	err := logx.Initialize(logx.LoggingConfig{
		Level:          "debug",
		ConsoleLogging: true,
		FileLogging:    true,
		Directory:      "/tmp/logs/myapp",
		Filename:       "myApp.log",
		MaxSize:        10, // MB
		MaxBackups:     10,
		MaxAge:         30,
		Compress:       true,
		UTC:            true, // pin timestamps to UTC
		IncludeCaller:  true, // annotate lines with source location
	})
	if err != nil {
		panic(err)
	}

	// Stamp persistent fields onto every line (As() and the slog bridge).
	logx.SetGlobalContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("build_commit", "abc123")
	})

	logx.As().Info().Msg("Application started")
	logx.As().Debug().Str("userID", "123").Msg("Debugging details")
	logx.As().Warn().Msg("This is a warning")
	logx.As().Error().Err(fmt.Errorf("test error")).Msg("An error occurred")
}
