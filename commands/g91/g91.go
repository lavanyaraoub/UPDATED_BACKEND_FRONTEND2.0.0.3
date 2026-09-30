package main

import (
	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

/*
Enable incremental/relative position mode
The table will rotate from the current position by the number of degrees specified. The
direction of motion depends on whether the specified increment is positive or negative (a
positive increment will result in a clockwise move and a negative increment will result in
a counter-clockwise move.)
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
	execContext.RunMode = "REL"
	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: "enable incremental/relative position mode"}
		results = append(results, result)
		channels.OpenWaitChannel()
		driverAction := channels.DriverAction{Action: "POSITION_MODE", Value: "REL"}
		channels.DriverActionChannel <- driverAction
		//wait for the signal back from driver that disabled shortest path set successfully
		channels.WaitTillCmdComplete()
	}
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "g91"
}

//Command used to lookup for plugins
var Command CommandHandler
