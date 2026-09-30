package main

import (
	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"errors"
	"strconv"
)

//CommandHandler type def
type CommandHandler struct {
}

//CreateHandler return an instance of rpm function
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle rpm command
func (r CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}
	rpm := cmd.GetValue()
	v, _ := strconv.ParseInt(rpm, 10, 32)

	if v > 20 {
		execContext.Err = errors.New("Feed rate value must be within 1 to 20")
		return results
	}

	if !execContext.TrialModeActive {

		result := dt.ExecutionResult{Description: "Set rpm to " + rpm}
		results = append(results, result)
		channels.OpenWaitChannel()
		driverAction := channels.DriverAction{Action: "SET_RPM", Value: rpm}
		//send the rpm value to driver action listener
		channels.DriverActionChannel <- driverAction
		//wait for the signal back from driver that rpm set successfully
		channels.WaitTillCmdComplete()
	}
	return results
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "rpm"
}

//Command used to lookup for plugins
var Command CommandHandler
