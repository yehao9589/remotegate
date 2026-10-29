package main

import (
	"context"
	"sync"
	"time"
)

type targetTask struct {
	ctx    context.Context
	cancel context.CancelFunc
}
type targetTasks struct{ pending sync.Map }

func (t *targetTasks) add(parent context.Context, id string, deadline int64) (*targetTask, bool) {
	if len(id) == 0 || len(id) > 128 {
		return nil, false
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if deadline > 0 {
		ctx, cancel = context.WithDeadline(parent, time.UnixMilli(deadline))
	} else {
		ctx, cancel = context.WithTimeout(parent, 55*time.Second)
	}
	task := &targetTask{ctx, cancel}
	if _, loaded := t.pending.LoadOrStore(id, task); loaded {
		cancel()
		return nil, false
	}
	return task, true
}
func (t *targetTasks) cancel(id string) {
	if task, ok := t.pending.Load(id); ok {
		task.(*targetTask).cancel()
	}
}
func (t *targetTasks) finish(id string) {
	if task, ok := t.pending.LoadAndDelete(id); ok {
		task.(*targetTask).cancel()
	}
}
