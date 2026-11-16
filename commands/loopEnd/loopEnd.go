package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

//CommandHandler type def
type CommandHandler struct {
}

//CreateHandler ends loop when encounter G10
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle generic exec
func (d CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	if execContext.TrialModeActive {
		execContext.MoveNextLine()
		return results
	}
	if execContext.LoopCount > 0 {
		execContext.LoopCount = execContext.LoopCount - 1
		execContext.NextCmdLineToExec = execContext.WhereLoopStarted + 1
	} else {
		execContext.MoveNextLine()
	}

	result := dt.ExecutionResult{Description: cmd.Description}
	results = append(results, result)
	return results
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "loopEnd"
}

//Command used to lookup for plugins
var Command CommandHandler
