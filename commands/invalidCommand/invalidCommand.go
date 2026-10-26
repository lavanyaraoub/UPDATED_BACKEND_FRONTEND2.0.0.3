package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"errors"
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
	execContext.Err = errors.New("Unable to execute command " + cmd.Cmd + " . No command handler found")
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "invalidCommand"
}

//Command used to lookup for plugins
var Command CommandHandler
