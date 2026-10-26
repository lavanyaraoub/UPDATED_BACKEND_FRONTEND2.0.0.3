package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"errors"
	"strings"
)

//CommandHandler for plugin
type CommandHandler struct {
}

//CreateHandler return an instance of divide 360 function
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle G0 command
func (d CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	params := strings.Split(cmd.Cmd, " ")
	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: cmd.Description}
		results = append(results, result)
	}
	if len(params) <= 1 {
		execContext.Err = errors.New("Feed rate not specified")
	}
	//iterate through the second index onwards
	//first is the start command
	for i := 1; i < len(params); i++ {
		paramToExec := execContext.CommandMaps.GetCommand(params[i])
		childCmd := dt.Command{Func: paramToExec.Func, Cmd: params[i], Description: paramToExec.Description}
		result := dt.ExecutionResult{Cmd: childCmd, ShouldExecute: true}
		results = append(results, result)
	}
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "g01"
}

//Command used to lookup for plugins
var Command CommandHandler
