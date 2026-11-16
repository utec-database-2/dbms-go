//go:build windows

package benchclock

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

type Stamp int64

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var counter = kernel32.NewProc("QueryPerformanceCounter")
var frequency = kernel32.NewProc("QueryPerformanceFrequency")
var ticksPerSecond = readFrequency()

func readFrequency() int64 {
	var f int64
	ok, _, err := frequency.Call(uintptr(unsafe.Pointer(&f)))
	if ok == 0 || f <= 0 {
		panic(fmt.Sprintf("QueryPerformanceFrequency: %v", err))
	}
	return f
}

func Now() Stamp {
	var ticks int64
	ok, _, err := counter.Call(uintptr(unsafe.Pointer(&ticks)))
	if ok == 0 {
		panic(fmt.Sprintf("QueryPerformanceCounter: %v", err))
	}
	return Stamp(ticks)
}

func Since(start Stamp) time.Duration {
	delta := int64(Now() - start)
	return time.Duration(float64(delta) * (1e9 / float64(ticksPerSecond)))
}

func Source() string { return fmt.Sprintf("Windows QPC (%d ticks/s)", ticksPerSecond) }
