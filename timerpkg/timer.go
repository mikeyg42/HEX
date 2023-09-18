package timerpkg

import (
	"context"
	"errors"
	"fmt"
	"time"

	zap "go.uber.org/zap"
)

type TimerLoggerKey struct{}

const turnTime = 30 * time.Second

type TimerControl struct {
	StartChan   chan struct{} // signal will originate
	StopChan    chan struct{}
	ForfeitChan chan struct{} // signal will originate from the dispatcher and be sent here
}

func MakeNewTimer(ctx context.Context) *TimerControl {
	tc := &TimerControl{
		StartChan:   make(chan struct{}),
		StopChan:    make(chan struct{}),
		ForfeitChan: make(chan struct{}),
	}

	go tc.ManageTimer(ctx)

	return tc
}

func (tc *TimerControl) StopTimer() {
	tc.StopChan <- struct{}{}
}

func (tc *TimerControl) StartTimer() {
	tc.StartChan <- struct{}{}
}

func (tc *TimerControl) ManageTimer(ctx context.Context) {
	timerLogger, ok := ctx.Value(TimerLoggerKey{}).(*zap.Logger) // cant include context bc I cant import storage bc circular. and this is totally fine - this is early development testing
	if !ok {
		panic(errors.New("could not get logger from context"))
	}

	var timer *time.Timer

	// tl() is a shortcut for timerLogger.Info()
	tl := timerLogger.Info

	for {
		select {
		case <-ctx.Done(): // handle the cancellation of the context
			tl("timer canceled by context")
			tf := timer.Stop() // no harm double checking this
			if !tf {
				<-timer.C // this explicitly drains the timer
			}

			return

		case <-tc.ForfeitChan:
			nowTime := time.Now()
			nowTime = nowTime.Round(time.Millisecond)
			fmt.Println("voluntary forfeit initiated")
			tf := timer.Stop()
			tl("Forfeit Cascade initiated because timer expired", zap.String("timeTimerOff", nowTime.Format("Mon Jan _2 15:04:05 2006")), zap.String("turnDuration", turnTime.String()))
			if !tf {
				<-timer.C // this explicitly drains the timer
			}

		case <-tc.StartChan:
			nowTime := time.Now().Round(time.Millisecond)
			futureTime := nowTime.Add(turnTime)
			nowTimeF := nowTime.Format("Mon Jan _2 15:04:05 2006")
			if timer != nil {
				tl("Timer start called while timer was non-zero", zap.String("nowTime", nowTimeF), zap.String("endTime", futureTime.Format("Mon Jan _2 15:04:05 2006")))
				if !timer.Stop() {
					<-timer.C
				}
			}

			timer = time.NewTimer(turnTime)
			tl("Timer initiated", zap.String("startTime", nowTimeF), zap.String("endTime", futureTime.Format("Mon Jan _2 15:04:05 2006")), zap.String("turnDuration", turnTime.String()))

		// this case block replaces <-timer.C, because it can cause a panic to read timer.C if its nil. so this prevents reading that value when its nil
		case <-func() <-chan time.Time {
			if timer != nil {
				return timer.C
			}
			return nil
		}():
			nowTime := time.Now().Round(time.Millisecond)
			tl("Forfeit Cascade initiated because timer expired", zap.String("timeTimerOff", nowTime.Format("Mon Jan _2 15:04:05 2006")), zap.String("turnDuration", turnTime.String()))
			return

		case <-tc.StopChan:
			nowTime := time.Now()
			nowTime = nowTime.Round(time.Millisecond)
			tf := timer.Stop() // this is false if the timer has already gone off or timer.Stop() was already called
			if !tf {
				tl("Timer stopped after its already gone off or timer.Stop() had already been called once before and no new timer started", zap.String("timeTimerOff", nowTime.Format("Mon Jan _2 15:04:05 2006")))
			} else {
				if timer != nil {
					tl("Turn Complete - a successful move was played before timer expired", zap.String("timeTimerOff", nowTime.Format("Mon Jan _2 15:04:05 2006")))
				} else {
					panic(errors.New("timer cannot be nil and also timer.stop() returned true"))
				}
			}
		}
	}
}
