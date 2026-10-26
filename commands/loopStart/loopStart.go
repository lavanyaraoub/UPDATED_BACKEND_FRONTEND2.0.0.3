package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

//CommandHandler type def
type CommandHandler struct {
}

//CreateHandler when line starts with R then loop starts
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle generic exec
func (d CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	loop, err := cmd.GetValueAsInt()
	execContext.WhereLoopStarted = execContext.CurrentExecCommandLine
	execContext.Err = err
	execContext.LoopCount = loop - 1
	execContext.MoveNextLine()

	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: cmd.Description}
		results = append(results, result)
	}
	return results
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "loopStart"
}

//Command used to lookup for plugins
var Command CommandHandler
