package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// The first screen reads persisted records before optional services can write
// or compete for CPU. The fallback also supports login launches with no frames.
// This gate is display scheduling only; it never grants execution authority.
type launchStartup struct {
	mu          sync.Mutex
	tasks       []*launchTask
	wake        chan struct{}
	workerDone  chan struct{}
	cancel      context.CancelFunc
	stopped     bool
	painted     chan struct{}
	once        sync.Once
	storedReads atomic.Bool
	envReady    atomic.Bool
}

func newLaunchStartup() *launchStartup {
	l := &launchStartup{painted: make(chan struct{})}
	l.storedReads.Store(true)
	return l
}

func (l *launchStartup) firstScreenPainted() {
	if l != nil {
		l.once.Do(func() { close(l.painted) })
	}
}

func (l *launchStartup) wait(ctx context.Context, fallback time.Duration) bool {
	if l == nil {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(fallback)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-l.painted:
	case <-timer.C:
	}
	return ctx.Err() == nil && !l.isStopped()
}

func (a *App) launchStoredReads() bool {
	return a != nil && a.launch != nil && a.launch.storedReads.Load()
}

type launchTask struct {
	name string
	run  func()
	done chan struct{}
}

// Finite warmups share one cooperative worker. Long-lived service loops retain
// their own cancellation; they are not placed on this initializer queue.
func (l *launchStartup) startWorker(parent context.Context) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || l.workerDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.workerDone = make(chan struct{})
	l.wake = make(chan struct{}, 1)
	go func() {
		defer func() {
			l.mu.Lock()
			l.stopped = true
			for _, task := range l.tasks {
				close(task.done)
			}
			l.tasks = nil
			close(l.workerDone)
			l.mu.Unlock()
		}()
		for {
			l.mu.Lock()
			var task *launchTask
			if len(l.tasks) > 0 {
				task = l.tasks[0]
				l.tasks = l.tasks[1:]
			}
			l.mu.Unlock()
			if task == nil {
				select {
				case <-ctx.Done():
					return
				case <-l.wake:
				}
				continue
			}
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			if ctx.Err() == nil {
				recordLaunchStage(task.name + "_start")
				task.run()
				recordLaunchStage(task.name + "_done")
			}
			close(task.done)
			if ctx.Err() != nil {
				return
			}
		}
	}()
}

func (a *App) deferLaunchTask(name string, run func()) <-chan struct{} {
	done := make(chan struct{})
	l := a.launch
	if l == nil {
		run()
		close(done)
		return done
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		close(done)
		return done
	}
	l.tasks = append(l.tasks, &launchTask{name: name, run: run, done: done})
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return done
}

// deferBackgroundLaunchTask waits its turn on the launch queue (after the
// first screen), then starts run in its own goroutine so long work such as
// model calls never holds the tasks queued behind it. run must stop on the
// app context.
func (a *App) deferBackgroundLaunchTask(name string, run func()) <-chan struct{} {
	return a.deferLaunchTask(name, func() { go run() })
}

// cancelPending fences new work without waiting for a native initializer.
// Shutdown can flush foreground data before joining the returned channel.
func (l *launchStartup) cancelPending() <-chan struct{} {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	l.firstScreenPainted()
	if l.cancel != nil {
		l.cancel()
	}
	for _, task := range l.tasks {
		close(task.done)
	}
	l.tasks = nil
	return l.workerDone
}

func (l *launchStartup) stop() {
	if done := l.cancelPending(); done != nil {
		<-done
	}
}

func (l *launchStartup) isStopped() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopped
}
