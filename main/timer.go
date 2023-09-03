package main

import (
	"fmt"
	"time"
)


type TimerControl struct {
	StartChan chan struct{}
	StopChan  chan struct{}
}

func MakeNewTimer() *TimerControl {
	tc := &TimerControl{
		StartChan: make(chan struct{}),
		StopChan:  make(chan struct{}),
	}

	go tc.ManageTimer()

	return tc
}

func (tc *TimerControl) StopTimer() {
	tc.StopChan <- struct{}{}
}

func (tc *TimerControl) StartTimer() {
	tc.StartChan <- struct{}{}
}

func (tc *TimerControl) ManageTimer() {
	var timer *time.Timer
	for {
		select {
		case <-tc.StartChan:
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(30 * time.Second)
		case <-tc.StopChan:
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
