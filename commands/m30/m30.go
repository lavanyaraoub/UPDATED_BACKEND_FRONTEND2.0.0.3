package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

/*
End of program
The code defines the end of Program. The Auto execution stops and cycle start command
is required to restart the program from beginning.
*/

//CommandHandler type def
type CommandHandler struct {
}

func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle disable shortest path
func (r CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: "end of program execution"}
		results = append(results, result)
	}
	execContext.EndExecution()
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "m30"
}

//Command used to lookup for plugins
var Command CommandHandler
