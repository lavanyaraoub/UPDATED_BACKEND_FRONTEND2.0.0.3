package main

import (
	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

/*
Enable Absolute position mode
The table will rotate from the current position to the specified target position which is
specifically within 360 degrees. The command value will be the actual degree marking on
the rotary table. G90 can be canceled by a G68 Short Path or G91 incremental positioning
command. G90 is the default mode.
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
	execContext.RunMode = "ABS"
	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: "enable absolute position mode"}
		results = append(results, result)
		channels.OpenWaitChannel()
		driverAction := channels.DriverAction{Action: "POSITION_MODE", Value: "ABS"}
		channels.DriverActionChannel <- driverAction
		//wait for the signal back from driver that disabled shortest path set successfully
		channels.WaitTillCmdComplete()
	}
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "g90"
}

//Command used to lookup for plugins
var Command CommandHandler
