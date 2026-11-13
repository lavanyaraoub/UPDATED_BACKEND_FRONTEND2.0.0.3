package main

import (
	dt "EtherCAT/datatypes"
	h "EtherCAT/commands"
)

//CommandHandler type def
type CommandHandler struct {
}

//CreateHandler return an instance of rpm function
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle generic exec
func (d CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	if cmd.Cmd == "G16" {
		execContext.Divide360On = 1
	} else if cmd.Cmd == "G10" {
		execContext.Divide360On = 2
	}
	if execContext.Divide360On == 1 {
		result := dt.ExecutionResult{Description: cmd.Description}
		results = append(results, result)
	} else {
		result := dt.ExecutionResult{Description: cmd.Description}
		results = append(results, result)
	}
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "divideBy360EnableDisable"
}

//Command used to lookup for plugins
var Command CommandHandler
