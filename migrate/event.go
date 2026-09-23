package migrate

import (
	"errors"
	"time"
)

// Event reports synchronous execution progress. State is started, succeeded,
// error, or unexecuted. Error means the call failed, not that the database made
// no changes: the driver error must distinguish rejection from an unknown result.
// SQL contains the trusted source at execution time. Statement is one-based for
// SQL events and zero for record operations. Events are not recovery instructions.
type Event struct {
	Time      time.Time
	Version   string
	Direction Direction
	Phase     string
	Statement int
	SQL       string
	State     string
	Err       error
}

func (r *Runner) emit(event Event) error {
	if r.config.OnEvent == nil {
		return nil
	}
	event.Time = time.Now().UTC()
	if err := r.config.OnEvent(event); err != nil {
		return &OperationError{Phase: "write execution progress", Version: event.Version, Statement: event.Statement, Cause: err}
	}
	return nil
}

func (r *Runner) operation(event Event, run func() error) error {
	event.State = "started"
	if err := r.emit(event); err != nil {
		return err
	}
	event.Err = run()
	event.State = "succeeded"
	if event.Err != nil {
		event.State = "error"
	}
	logErr := r.emit(event)
	if event.Err != nil {
		return errors.Join(&OperationError{Phase: event.Phase, Version: event.Version, Statement: event.Statement, Cause: event.Err}, logErr)
	}
	return logErr
}
