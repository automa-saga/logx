package main

import (
	"fmt"
	"github.com/automa-saga/logx"
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
	})
	if err != nil {
		panic(err)
	}

	logx.As().Info().Msg("Application started")
	logx.As().Debug().Str("userID", "123").Msg("Debugging details")
	logx.As().Warn().Msg("This is a warning")
	logx.As().Error().Err(fmt.Errorf("test error")).Msg("An error occurred")
}
