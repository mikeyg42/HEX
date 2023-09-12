package retry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"go.uber.org/zap"
)

const maxRetries = 3

// last output of the function to be retried MUST be an error. and the function MUST return no more than 3 outputs (error included)

// RResult represents the result of a function execution, encapsulating any error that occurred and a message.
type RResult struct {
	Err       error
	Message   string
	Interface interface{}
}

// This assumes you have set logger in context using ctx.WithValue and a corresponding key.
func GetLoggerFromContext(ctx context.Context) *zap.Logger {
	logger, ok := ctx.Value("logger").(*zap.Logger)
	if !ok {
		return zap.NewNop()
	}
	return logger
}

func RetryFunc(ctx context.Context, function interface{}) *RResult {
	typ := reflect.TypeOf(function)
	if typ.Kind() != reflect.Func {
		return &RResult{
			Err:       fmt.Errorf("provided argument is not a function"),
			Message:   "Not a function",
			Interface: nil,
		}
	}

	numOut := typ.NumOut()

	// we get the logger out of context within the subfunctions of the switch block, so don't try to log anything in this space...

	// A decision based on number of outputs and their types
	switch numOut {
	case 1:
		if isErrorType(typ.Out(0)) {
			return retryWithError(ctx, function.(func() error))
		}
	case 2:
		// Here we are assuming the two return types are string and error
		if isErrorType(typ.Out(1)) {
			return retryWithResultAndError(ctx, function.(func() (string, error)))
		}
	case 3:
		// Here we are assuming the three return types are string, interface, and error
		if isErrorType(typ.Out(2)) {
			return retryWithThree(ctx, function.(func() (string, interface{}, error)))
		}
	default:
		return &RResult{
			Err:       fmt.Errorf("unsupported function signature"),
			Message:   "Unsupported function signature",
			Interface: nil,
		}
	}
	// If we reached here, it means we couldn't find a matching signature.
	return &RResult{
		Err:       errors.New("no matching function signature found"),
		Message:   "Unsupported function signature",
		Interface: nil,
	}
}

func retryWithError(ctx context.Context, function func() error) *RResult {
	logger := GetLoggerFromContext(ctx)
	for i := 0; i < maxRetries; i++ {
		if err := function(); err == nil {
			logger.Info("Function executed successfully")
			return &RResult{Err: nil, Message: "Success", Interface: nil}
		}
		select {
		case <-ctx.Done():
			logger.Error("Context done", zap.Error(ctx.Err()))
			return &RResult{Err: ctx.Err(), Message: "Context finished before retries could complete", Interface: nil}
		case <-time.After(time.Millisecond * 100 * time.Duration(1<<i)):
		}
	}
	logger.Error("Failed after all retries")
	return &RResult{Err: fmt.Errorf("failed after %d attempts", maxRetries), Message: "Failure after retries", Interface: nil}
}

func retryWithResultAndError(ctx context.Context, function func() (string, error)) *RResult {
	logger := GetLoggerFromContext(ctx)
	for i := 0; i < maxRetries; i++ {
		msg, err := function()
		if err == nil {
			logger.Info("Function executed successfully")
			return &RResult{Err: nil, Message: msg, Interface: nil}
		}
		logger.Warn("Attempt failed", zap.String("message", msg), zap.Error(err), zap.Int("attempt", i+1))

		select {
		case <-ctx.Done():
			logger.Error("Context done", zap.Error(ctx.Err()))
			return &RResult{Err: ctx.Err(), Message: "Context finished before retries could complete", Interface: nil}
		case <-time.After(time.Millisecond * 100 * time.Duration(1<<i)):
		}
	}
	logger.Error("Failed after all retries")
	return &RResult{Err: fmt.Errorf("failed after %d attempts", maxRetries), Message: "Failure after retries", Interface: nil}
}

func retryWithThree(ctx context.Context, function func() (string, interface{}, error)) *RResult {

	logger := GetLoggerFromContext(ctx)

	for i := 0; i < maxRetries; i++ {
		msg, intface, err := function()
		if err == nil {
			logger.Info("Function executed successfully", zap.String("message", msg))
			return &RResult{Err: nil, Message: msg, Interface: intface}
		}
		logger.Warn("Attempt failed", zap.String("message", msg), zap.Any("interface", intface), zap.Error(err), zap.Int("attempt", i+1))

		select {
		case <-ctx.Done():
			logger.Error("Context done", zap.Error(ctx.Err()))
			return &RResult{Err: ctx.Err(), Message: "Context finished before retries could complete", Interface: intface}
		case <-time.After(time.Millisecond * 100 * time.Duration(1<<i)):
		}
	}
	logger.Error("Failed after all retries")
	return &RResult{Err: fmt.Errorf("failed after %d attempts", maxRetries), Message: "Failure after retries", Interface: nil}
}

// helper function to check if an output of the function is an error
func isErrorType(t reflect.Type) bool {
	return t == reflect.TypeOf((*error)(nil)).Elem()
}
