package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

/*
Continuous Execution / Infinite loop.
It makes the program to execute from the beginning again. This is useful for creating
programs that have to perform the same cycle over and over again unless stopped.
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
	if execContext.TrialModeActive {
		execContext.EndExecution()
		return results
	}
	result := dt.ExecutionResult{Description: "start run code from beginning"}
	results = append(results, result)
	execContext.MoveToStart()
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "m99"
}

//Command used to lookup for plugins
var Command CommandHandler
