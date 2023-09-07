package retry

import (
	"context"
	"fmt"
	"reflect"
	"time"
	"errors"
)

const maxRetries = 3

// RResult represents the result of a function execution, encapsulating any error that occurred and a message.
type RResult struct {
	Err     error
	Message string
}


func RetryFunc(ctx context.Context, function interface{}) *RResult {
	typ := reflect.TypeOf(function)
	if typ.Kind() != reflect.Func {
		return &RResult{
			Err:     fmt.Errorf("provided argument is not a function"),
			Message: "Not a function",
		}
	}

	numOut := typ.NumOut()

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
		// Here we are assuming the three return types are string, int, and error
		if isErrorType(typ.Out(2)) {
			return retryWithResultAndIntAndError(ctx, function.(func() (string, int, error)))
		}
	default:
		return &RResult{
			Err:     fmt.Errorf("unsupported function signature"),
			Message: "Unsupported function signature",
		}
	}
	// If we reached here, it means we couldn't find a matching signature.
	return &RResult{
		Err:     errors.New("no matching function signature found"),
		Message: "Unsupported function signature",
	}
}

func retryWithError(ctx context.Context, function func() error) *RResult {
	for i := 0; i < maxRetries; i++ {
		if err := function(); err == nil {
			return &RResult{Err: nil, Message: "Success"}
		}
		time.Sleep(time.Millisecond * 100 * time.Duration(1<<i))
	}
	return &RResult{Err: fmt.Errorf("failed after %d attempts", maxRetries), Message: "Failure after retries"}
}

func retryWithResultAndError(ctx context.Context, function func() (string, error)) *RResult {
	for i := 0; i < maxRetries; i++ {
		msg, err := function()
		if err == nil {
			return &RResult{Err: nil, Message: msg}
		}
		time.Sleep(time.Millisecond * 100 * time.Duration(1<<i))
	}
	return &RResult{Err: fmt.Errorf("failed after %d attempts", maxRetries), Message: "Failure after retries"}
}

func isErrorType(t reflect.Type) bool {
	return t == reflect.TypeOf((*error)(nil)).Elem()
}
