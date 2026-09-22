package main

import (
	channels "EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"EtherCAT/helper"
	"errors"
	"strings"
	"fmt"
	"strconv"
	"time"
)

//CommandHandler type def
type CommandHandler struct {
}

//CreateHandler return an instance of move rotary function
func CreateHandler() h.Handler {
	return CommandHandler{}
}

//Handle generic exec
func (d CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	drvSetting := execContext.DriveSettings[cmd.GetCommandFirstChar()]
	setWorkOffset := 0.0
	if execContext.CurrentWorkOffSet != "" {
		setWorkOffset = drvSetting.ConfiguredWorkOffset[execContext.CurrentWorkOffSet]
	}
	results := []dt.ExecutionResult{}
	pos := cmd.GetValue()
	driveName := execContext.ExtractString(cmd.Cmd)
// ---- Axis Routing Patch (Correct & Compilable) ----
if strings.HasPrefix(strings.ToUpper(cmd.Cmd), "A") {
    driveName = "A"
}
if strings.HasPrefix(strings.ToUpper(cmd.Cmd), "B") {
    driveName = "B"
}
	posFloat, _ := strconv.ParseFloat(pos, 64)
	posFloat = posFloat + float64(setWorkOffset)
	posWithOffset := fmt.Sprintf("%f", posFloat)
	err := checkPotNotLimit(execContext, posFloat, cmd.GetCommandFirstChar())
	if err != nil {
		execContext.Err = err
		return results
	}

	// logger.Trace("Pos:", cmd.GetValue(), ", work offset:", execContext.CurrentWorkOffSet, ", offset degree:", setWorkOffset, ", with workoffset: ", posWithOffset)
	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{ShouldExecute: false, Description: "Moved Motor: " + strconv.Itoa(cmd.DriveID) + " to position " + posWithOffset}
		results = append(results, result)

		channels.OpenWaitChannel()
		setWorkOffset := channels.DriverAction{Action: "SET_WORK_OFFSET", Value: fmt.Sprintf("%f", setWorkOffset), DriveName: driveName}
		channels.DriverActionChannel <- setWorkOffset

		//if this delay is not there then setting the workoffset will take time and destination position will
		//show without workoffset.
		time.Sleep(time.Duration(40) * time.Microsecond)

		driverAction := channels.DriverAction{Action: "MOVE_TO_POSITION", Value: posWithOffset, DriveName: driveName}
		channels.DriverActionChannel <- driverAction
		channels.WaitTillCmdComplete()
	}
	execContext.MoveNextLine()
	return results
}

func checkPotNotLimit(execContext *dt.ExecutionContext, targetPos float64, driveName string) error {
	drvSetting := execContext.DriveSettings[driveName]
	if drvSetting.POTLimit <= 0 && drvSetting.NOTLimit <= 0 {
		return nil
	}

	// rawDest is the unwrapped destination used for limit comparison.
	// For ABS moves GetAbsolutePosition normalises to [0,360) so negative
	// targets like -90° become 270° — we must compare the original targetPos
	// directly against the NOT limit in that case.
	// For REL moves we accumulate from the previous destination without
	// wrapping so the sign is preserved for limit checking.
	var rawDest float64
	if execContext.RunMode == "ABS" {
		// For ABS mode use targetPos directly for limit comparison — it carries
		// the sign the user intended (e.g. -90° means "move to -90°").
		rawDest = targetPos
		_, drvSetting.DestinationPosition = helper.GetAbsolutePosition(drvSetting.DestinationPosition, targetPos, false)
	} else {
		// REL: accumulate without wrapping to preserve sign for limit check.
		rawDest = drvSetting.DestinationPosition + targetPos
		_, drvSetting.DestinationPosition = helper.GetRelativePosition(drvSetting.DestinationPosition, targetPos, drvSetting.DestinationPosition)
	}

	if rawDest >= 0 {
		if drvSetting.DestinationPosition >= float64(drvSetting.POTLimit) {
			return errors.New("POT Limit exceeded")
		}
	} else {
		if rawDest <= float64(drvSetting.NOTLimit) {
			return errors.New("NOT Limit exceeded")
		}
	}
	execContext.DriveSettings[driveName] = drvSetting
	return nil
}

//CommandName name of the command
func (d CommandHandler) CommandName() string {
	return "moveRotaryDegree"
}

//Command used to lookup for plugins
var Command CommandHandler
