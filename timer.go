package main

import(
	"fmt"
	"time"

)

type TimerControl struct {
    startChan chan struct{}
    stopChan  chan struct{}
}

func NewTimerControl() *TimerControl {
    return &TimerControl{
        startChan: make(chan struct{}),
        stopChan:  make(chan struct{}),
    }
}

func (tc *TimerControl) StartTimer(d *Dispatcher, string, nextMoveNumber int, gameID string) {
    var nextPlayerID string
	if nextMoveNumber%2 == 1 {
		nextPlayerID = "P1"
	} else {
		nextPlayerID = "P2"
	}
	newCmd := &StartNextTurnCmd{
		GameID: gameID,
		nextMoveNumber: nextMoveNumber,
		nextPlayerID: nextPlayerID,
		starttime: time.Now(),
	}
	d.commandDispatcher.Dispatch(ctx, newCmd)
    tc.startChan <- struct{}{}
}

func (tc *TimerControl) StopTimer() {
    tc.stopChan <- struct{}{}
}

func (tc *TimerControl) ManageTimer() {
    for {
        select {
        case <-tc.startChan:
            go func() {
                select {
                case <-time.After(30 * time.Second):
                    fmt.Println("Forfeit Event Cascade")
                case <-tc.stopChan:
                    fmt.Println("Next Turn")
                }
            }()
        }
    }
}




