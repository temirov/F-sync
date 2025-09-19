package server

import (
	"fmt"
	"sync"
)

const (
	handleResolutionTaskPrefix          = "task-"
	handleResolutionStatusRunning       = handleResolutionStatus("running")
	handleResolutionStatusCompleted     = handleResolutionStatus("completed")
	handleResolutionStatusFailed        = handleResolutionStatus("failed")
	handleResolutionTaskNotFoundMessage = "comparison task not found"
)

// handleResolutionStatus represents the lifecycle state of a handle resolution task.
type handleResolutionStatus string

// handleResolutionTask captures state for a handle resolution execution.
type handleResolutionTask struct {
	identifier string
	total      int
	completed  int
	status     handleResolutionStatus
	errors     map[string]string
}

// handleResolutionTaskSnapshot copies the public portions of a task for serialization.
type handleResolutionTaskSnapshot struct {
	Identifier string
	Total      int
	Completed  int
	Status     handleResolutionStatus
	Errors     map[string]string
}

// handleResolutionTracker tracks active and completed resolution tasks.
type handleResolutionTracker struct {
	mutex        sync.Mutex
	tasks        map[string]*handleResolutionTask
	nextSequence int
}

// newHandleResolutionTracker constructs a tracker with empty state.
func newHandleResolutionTracker() *handleResolutionTracker {
	return &handleResolutionTracker{tasks: make(map[string]*handleResolutionTask)}
}

// CreateTask registers a new resolution task and returns its snapshot.
func (tracker *handleResolutionTracker) CreateTask(total int) handleResolutionTaskSnapshot {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	tracker.nextSequence++
	identifier := fmt.Sprintf("%s%d", handleResolutionTaskPrefix, tracker.nextSequence)
	task := &handleResolutionTask{
		identifier: identifier,
		total:      total,
		status:     handleResolutionStatusRunning,
		errors:     make(map[string]string),
	}
	tracker.tasks[identifier] = task
	return tracker.snapshotTask(task)
}

// RecordResolution updates task progress for an account identifier and optional error.
func (tracker *handleResolutionTracker) RecordResolution(taskIdentifier string, accountIdentifier string, resolutionErr error) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	task, exists := tracker.tasks[taskIdentifier]
	if !exists {
		return
	}
	if resolutionErr != nil {
		task.errors[accountIdentifier] = resolutionErr.Error()
	}
	task.completed++
	if task.completed > task.total {
		task.completed = task.total
	}
}

// CompleteTask transitions a task to its terminal status.
func (tracker *handleResolutionTracker) CompleteTask(taskIdentifier string, failed bool) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	task, exists := tracker.tasks[taskIdentifier]
	if !exists {
		return
	}
	if failed {
		task.status = handleResolutionStatusFailed
	} else {
		task.status = handleResolutionStatusCompleted
	}
	if task.completed < task.total {
		task.completed = task.total
	}
}

// TaskSnapshot returns a copy of the task state for external observers.
func (tracker *handleResolutionTracker) TaskSnapshot(taskIdentifier string) (handleResolutionTaskSnapshot, bool) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	task, exists := tracker.tasks[taskIdentifier]
	if !exists {
		return handleResolutionTaskSnapshot{}, false
	}
	return tracker.snapshotTask(task), true
}

func (tracker *handleResolutionTracker) snapshotTask(task *handleResolutionTask) handleResolutionTaskSnapshot {
	clonedErrors := make(map[string]string, len(task.errors))
	for accountIdentifier, message := range task.errors {
		clonedErrors[accountIdentifier] = message
	}
	return handleResolutionTaskSnapshot{
		Identifier: task.identifier,
		Total:      task.total,
		Completed:  task.completed,
		Status:     task.status,
		Errors:     clonedErrors,
	}
}
