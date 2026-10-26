package main

import (
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"strings"
)

/**
All the commanded position values in the program will be added with values in the respective
commands. These values are configured in settings.json file.
**/

//CommandHandler type def
type CommandHandler struct {
}

//CreateHandler return an instance of workoffset function
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle workoffset command
func (r CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	// offset := execContext.ConfiguredWorkOffset[cmd.Cmd]
	params := strings.Split(cmd.Cmd, " ")
	execContext.CurrentWorkOffSet = params[0]
	result := dt.ExecutionResult{Description: cmd.Description}
	results = append(results, result)
	if len(params) > 1 {
		//iterate through the second index onwards, some times workoffset line can have Rotary command as well.
		for i := 1; i < len(params); i++ {
			paramToExec := execContext.CommandMaps.GetCommand(params[i])
			childCmd := dt.Command{Func: paramToExec.Func, Cmd: params[i], Description: paramToExec.Description, ConsiderInBlockExecution: paramToExec.ConsiderInBlockExecution}
			result := dt.ExecutionResult{Cmd: childCmd, ShouldExecute: true}
			results = append(results, result)
		}
	}

	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "workoffset"
}

//Command used to lookup for plugins
var Command CommandHandler
