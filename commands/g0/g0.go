package main

import (
	channels "EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
)

//CommandHandler for plugin
type CommandHandler struct {
}

//CreateHandler return an instance of divide 360 function
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle G01 command
func (d CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: cmd.Description}
		results = append(results, result)
		channels.OpenWaitChannel()
		driverAction := channels.DriverAction{Action: "SET_RPM", Value: "20"}
		//send the rpm value to driver action listener
		channels.DriverActionChannel <- driverAction

		//wait for the signal back from driver that rpm set successfully
		channels.WaitTillCmdComplete()
	}
	execContext.MoveNextLine()
	return results
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "g0"
}

//Command used to lookup for plugins
var Command CommandHandler
