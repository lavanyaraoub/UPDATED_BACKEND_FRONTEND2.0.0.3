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
G16. P xx
Macro for dividing 360 Degrees equally
This command divides 360 degrees equally by the number followed by G16
command. This command shall execute individual segment degrees received after
dividing 360 degrees in multiple steps. The P values will always be a integer and
no fractional values shall be allowed. The after dividing with 360 only three
decimal points can be used for command. Any other fractional values beyond 3
decimal pints shall be held in memory for further calculations.
This single command operates in multiple steps. Hence multiple Cycle start
commands will be required to complete this function in Single block /ECS
parameter enable condition.
This command enables user to avoid writing multiples program lines. This
command will only work in incremental Positioning (G91). Before command G16,
G91 should have already been commanded in the program if not then compiler
error should appear. The P value can be + and - . + will move the degrees in
clockwise direction of the rotary table and – will move the rotary table in counter
clockwise direction.
Format:
G16 Pxx;
“G16” 360 divide command, “Pxx” divider value
Example
G16 P10;
360/10 = 36 degrees commanded in 10 steps
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
		execContext.Err = errors.New("G16 can be run only in incremental positioning. Apply G91 before G16")
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
		result := dt.ExecutionResult{Description: fmt.Sprintf("macro for dividing 360 degree segments equally. Divided Deg: %f Divisor: %d", degreeDivided, divisor)}
		results = append(results, result)
		counter = execContext.CurrentLoopCounter
		//for i := tmpCurCounter; i < execContext.LoopCount; i++ {
		for counter < execContext.LoopCount {
			fmt.Println("wait for next block div 360")
			channels.OpenWaitChannel()

			driverAction := channels.DriverAction{Action: "MOVE_TO_POSITION", Value: fmt.Sprintf("%f", degreeDivided)}
			counter = counter + 1
			execContext.CurrentLoopCounter = counter
			channels.DriverActionChannel <- driverAction
			//wait for the signal back from driver that rotary is moved to position
			channels.WaitTillCmdComplete()

			if cmd.ConsiderInBlockExecution == 1 && counter < execContext.LoopCount {
				fmt.Println("G16 wait for next block")
				execContext.WaitExecuteNextCommand()
			}
			if execContext.StopExecution {
				break
			}
		}

	}
	if !execContext.StopExecution {
		execContext.LoopCount = 0
		execContext.CurrentLoopCounter = 0
	}
	execContext.MoveNextLine()
	return results
}

func getDividedDegreeAndTimes(cmd string, execContext *dt.ExecutionContext) (float64, int, error) {
	params := strings.Split(cmd, " ")
	if len(params) <= 1 {
		err := errors.New("Invalid parameters for G16 code")
		return 0, 0, err
	}
	divisorVal := params[1]

	divisor, divisorErr := execContext.ExtractNumericAsInt(divisorVal) //strconv.Atoi(divisorVal[1:len(divisorVal)])
	if divisorErr != nil {
		return 0, 0, divisorErr
	}
	degreeDivided := 360 / float64(divisor)
	return degreeDivided, divisor, nil
}

//CommandName name of the command
func (r CommandHandler) CommandName() string {
	return "g16"
}

//Command used to lookup for plugins
var Command CommandHandler
