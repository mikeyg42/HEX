package main

import (
	"fmt"
	"time"
)

type TimerControl struct {
	startChan chan struct{}
	stopChan  chan struct{}
}

func MakeNewTimer() *TimerControl {
	tc := &TimerControl{
		startChan: make(chan struct{}),
		stopChan:  make(chan struct{}),
	}

	go tc.ManageTimer()

	return tc
}

func (tc *TimerControl) StopTimer() {
	tc.stopChan <- struct{}{}
}

func (tc *TimerControl) StartTimer() {
	tc.startChan <- struct{}{}
}

func (tc *TimerControl) ManageTimer() {
	var timer *time.Timer
	for {
		select {
		case <-tc.startChan:
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(30 * time.Second)
		case <-tc.stopChan:
			if timer != nil {
				timer.Stop()
				fmt.Println("Turn Complete")
			}
		case <-timer.C:
			if timer != nil {
				fmt.Println("Forfeit Event Cascade")
			}
		}
	}
}
