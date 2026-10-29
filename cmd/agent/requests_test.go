package main

import (
	"context"
	"testing"
	"time"
)

func TestRequestTrackerCancellationAndExpiry(t *testing.T) {
	var tasks targetTasks
	task, ok := tasks.add(context.Background(), "queued", time.Now().Add(time.Minute).UnixMilli())
	if !ok {
		t.Fatal("task not tracked")
	}
	if _, ok := tasks.add(context.Background(), "queued", 0); ok {
		t.Fatal("duplicate task replaced original")
	}
	tasks.cancel("queued")
	if task.ctx.Err() != context.Canceled {
		t.Fatal("queued cancellation ignored")
	}
	tasks.finish("queued")
	if _, ok := tasks.pending.Load("queued"); ok {
		t.Fatal("completed task retained")
	}
	old, ok := tasks.add(context.Background(), "expired", time.Now().Add(-time.Second).UnixMilli())
	if !ok || old.ctx.Err() != context.DeadlineExceeded {
		t.Fatal("expired task admitted")
	}
	tasks.finish("expired")
}
