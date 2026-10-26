package main

import (
	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

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
		result := dt.ExecutionResult{Description: "disabling shortest path", ShouldExecute: false}
		results = append(results, result)
		channels.OpenWaitChannel()
		driverAction := channels.DriverAction{Action: "SHORTEST_PATH_ENABLED", Value: "false"}
		channels.DriverActionChannel <- driverAction
		//wait for the signal back from driver that disabled shortest path set successfully
		channels.WaitTillCmdComplete()
	}
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "g69"
}

//Command used to lookup for plugins
var Command CommandHandler
