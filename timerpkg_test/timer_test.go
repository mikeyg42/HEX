package timerpkg_tests

import (
	"context"
	"testing"
	"time"

	timerpkg "github.com/mikeyg42/HEX/timerpkg"
	zap "go.uber.org/zap"
)

func TestTimer_StartAndStop(t *testing.T) {
	// Mock logger
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	ctx := context.WithValue(context.Background(), timerpkg.TimerLoggerKey{}, logger)

	timer := timerpkg.MakeNewTimer(ctx)

	// Start the timer
	timer.StartTimer()

	// Sleep for a duration less than the timer's total duration
	time.Sleep(10 * time.Second)

	// Stop the timer
	timer.StopTimer()

	// Sleep for a duration greater than the timer's remaining duration to ensure it doesn't trigger the expiration
	time.Sleep(25 * time.Second)

	// ... Check any expected behaviors here.
	// For example, you could use channels to signal from your timer code to this test to assert certain behaviors occurred or did not occur.
}

func TestTimer_Expiration(t *testing.T) {
	// Mock logger
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	ctx := context.WithValue(context.Background(), TimerLoggerKey{}, logger)

	timer := MakeNewTimer(ctx)

	// Start the timer
	timer.StartTimer()

	// Sleep for a duration greater than the timer's total duration to let it expire
	time.Sleep(35 * time.Second)

	// ... Check any expected behaviors here.
	// For example, check that the forfeit logic was triggered.
}

// ... more tests for other behaviors ...
