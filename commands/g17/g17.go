package main

import (
	"EtherCAT/channels"
	h "EtherCAT/commands"
	dt "EtherCAT/datatypes"
	"errors"
	"fmt"
	"math"
	"strings"
)

/*
Macro for dividing Degree Segments equally

This command divides segment degrees specified in X equally by the number P
followed by G17 command. This command shall execute individual segment
degrees received after dividing “X” degrees in multiple steps. The P values will
always be a integer and no fractional values shall be allowed. After dividing the
segments three decimal points can be used for command the servo amplifier. Any
other fractional values beyond 3 decimal pints shall be held in memory for further
calculations.
This single command operates in multiple steps. Hence multiple Cycle start
commands will be required to complete this function in Single block /ECS
parameter enable condition.
This command enables user to avoid writing multiples program lines. This
command will only work in incremental Positioning (G91). Before command G17,
G91 should have already been commanded in the program if not then compiler
error should appear.

Format:
G17 Xxx Pxx
G17 segment division on, X segment value in degrees , P divisor number.
G17 X90 P2
90 degrees / 2 = 45 degrees.
Command will move 45 degrees each time for 2 times before the command is
completed and the program pointer moves to the next line.
*/

//CommandHandler type def
type CommandHandler struct {
}

func CreateHandler() h.Handler {
	return CommandHandler{}
}

var counter int

//Handle perform g17 command
func (r CommandHandler) Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult {
	results := []dt.ExecutionResult{}

	if execContext.RunMode != "REL" {
		execContext.Err = errors.New("G17 can be run only in incremental positioning. Apply G91 before G17")
		return results
	}
	degreeDivided, divisor, err := getDividedDegreeAndTimes(cmd.Cmd, execContext)
	if err != nil {
		execContext.Err = err
		return results
	}

	//if the exec context have loop count greater than 0 means, its running after a restart
	//so use the previous run value and start from where it left
	if execContext.LoopCount <= 0 {
		execContext.LoopCount = int(math.Abs(float64(divisor)))
		execContext.CurrentLoopCounter = 0
	}

	if !execContext.TrialModeActive {
		result := dt.ExecutionResult{Description: "macro for dividing degree segments equally"}
		results = append(results, result)
		counter = execContext.CurrentLoopCounter
		// for i := execContext.CurrentLoopCounter; i < execContext.LoopCount; i++ {
		for counter < execContext.LoopCount {
			channels.OpenWaitChannel()
			driverAction := channels.DriverAction{Action: "MOVE_TO_POSITION", Value: fmt.Sprintf("%f", degreeDivided)}
			counter = counter + 1
			execContext.CurrentLoopCounter = counter
			channels.DriverActionChannel <- driverAction

			//wait for the signal back from driver that rotary is moved to position
			channels.WaitTillCmdComplete()

			if cmd.ConsiderInBlockExecution == 1 && counter < execContext.LoopCount {
				fmt.Println("G17 wait for next block")
				execContext.WaitExecuteNextCommand()
			}
			if execContext.StopExecution {
				break
			}
		}
		fmt.Println("G17 completed")
	}
	if !execContext.StopExecution {
		fmt.Println("loop count resetted")
		execContext.LoopCount = 0
		execContext.CurrentLoopCounter = 0
	}
	execContext.MoveNextLine()
	return results
}

func getDividedDegreeAndTimes(cmd string, execContext *dt.ExecutionContext) (float64, int, error) {
	params := strings.Split(cmd, " ")
	if len(params) <= 2 {
		err := errors.New("Invalid parameters for G17 code")
		return 0, 0, err
	}
	driverNameAndDegree := params[1]
	divisorVal := params[2]

	degreeToDivide, degreeToDivideErr := execContext.ExtractNumericAsFloat(driverNameAndDegree) //strconv.ParseFloat(driverNameAndDegree[1:len(driverNameAndDegree)], 32)
	if degreeToDivideErr != nil {
		return 0, 0, degreeToDivideErr
	}

	divisor, divisorErr := execContext.ExtractNumericAsInt(divisorVal) //strconv.Atoi(divisorVal[1:len(divisorVal)])
	if divisorErr != nil {
		return 0, 0, divisorErr
	}
	degreeDivided := degreeToDivide / float64(divisor)
	return degreeDivided, divisor, nil
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "g17"
}

//Command used to lookup for plugins
var Command CommandHandler
