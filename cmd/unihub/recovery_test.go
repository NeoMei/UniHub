package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecoveryCycleIsolatesProviderTimeout(t *testing.T) {
	financialRan := make(chan struct{})
	err := runRecoveryCycle(context.Background(), 30*time.Millisecond,
		func(ctx context.Context) error {
			<-ctx.Done()
			select {
			case <-financialRan:
			default:
				t.Error("provider timeout delayed ordinary financial recovery")
			}
			return ctx.Err()
		},
		func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Error("financial recovery inherited an expired provider context")
			}
			close(financialRan)
			return nil
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("provider error not retained: %v", err)
	}
}
