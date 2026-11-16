package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"strconv"
	"time"
)

/*
Introduce a delay before the next command is executed. D command can be used for
creating a delay in part program.

Delay is in seconds
*/

//CommandHandler type def
type CommandHandler struct {
}

//CreateHandler when line starts with R then loop starts
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle generic exec
func (d CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	delay, err := cmd.GetValueAsInt()
	results := []dt.ExecutionResult{}
	if !execContext.TrialModeActive {
		// Use a cancellable wait instead of one unconditional Sleep. Reset()
		// clears StopExecution, so the immutable RunID generation is required
		// to prevent this old handler from executing the next program line.
		runID := execContext.RunID
		deadline := time.Now().Add(time.Duration(delay) * time.Second)
		for time.Now().Before(deadline) {
			cancelled := execContext.StopExecution ||
				(runID != 0 && dt.GetCancelGeneration() >= runID) ||
				(runID != 0 && !dt.ExecController.IsCurrent(runID))
			if cancelled {
				execContext.StopExecution = true
				return results
			}

			remaining := time.Until(deadline)
			tick := 50 * time.Millisecond
			if remaining < tick {
				tick = remaining
			}
			time.Sleep(tick)
		}
		result := dt.ExecutionResult{Description: "Delay for " + strconv.Itoa(delay) + " seconds"}
		results = append(results, result)
	}
	execContext.Err = err
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "delay"
}

//Command used to lookup for plugins
var Command CommandHandler