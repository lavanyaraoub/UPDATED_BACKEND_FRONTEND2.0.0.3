package main

import (
	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

/*
Enable shortest path
Shortest path mode. The table will rotate from the current position to the
target position taking the shortest path (CW or CCW depending on which ever is shorter). In this
mode, the position has to be between -360 degrees and 360degrees.
*/

//CommandHandler type def
type CommandHandler struct {
}

func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle enabling shortest path
func (r CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: "enabling shortest path", ShouldExecute: false}
		results = append(results, result)
		channels.OpenWaitChannel()
		driverAction := channels.DriverAction{Action: "SHORTEST_PATH_ENABLED", Value: "true"}
		channels.DriverActionChannel <- driverAction
		//wait for the signal back from driver that enabled shortest path set successfully
		channels.WaitTillCmdComplete()
	}
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "g68"
}

//Command used to lookup for plugins
var Command CommandHandler
