package executors

import (
	channels "EtherCAT/channels"
	dt "EtherCAT/datatypes"
	logger "EtherCAT/logger"
)

/*
This listener will listen to input commands to command executors when in the
middle of executing a program. For e.g. user can change the execution to
single or continuous. Send emergency stop or stop execution of the program etc.
*/

// init pre-creates CommandExecInputChannel so that any caller (alarm poller,
// GPIO handlers, motor-driver events) that fires before Initialize() is called
// never sees a nil channel.  The buffered capacity matches the one previously
// created inside listenCommandExecInput — 10 slots is enough to absorb a burst
// of stop/reset signals without blocking the sender goroutine.
//
// WHY: InitMaster() starts the PDO cyclic task and alarm poller before
// Initialize() runs (~800 ms later).  If a drive fault fires in that window
// and DriverError() calls WriteCommandExecInput("stop_prog_exec"), it found a
// nil channel and had to silently drop the command, leaving the executor in an
// inconsistent state.  Creating the channel here, at import time, closes that
// race entirely without changing any other call order.
func init() {
	channels.CommandExecInputChannel = make(chan channels.CommandExecInput, 10)
}

func listenCommandExecInput(execContext *dt.ExecutionContext) {
	// Channel is already created by init() above. We only start the consumer
	// goroutine here, once a valid execContext is available.
	go func(execContextToModify *dt.ExecutionContext) {
		for {
			msg := <-channels.CommandExecInputChannel
			switch msg.InputType {

			case "command_exec_mode":
				execContextToModify.ExecutionMode = msg.Data
				logger.Trace("change command exec mode to ", msg.Data)

			case "move_next_line":
				channels.NotifySingleModeComplete()
				logger.Trace("move to next line commanded from ui")

			case "stop_prog_exec":
				logger.Trace("stop_prog_exec received")
				execContextToModify.StopExecution = true
				dt.ExecController.Cancel()
				channels.NotifyCmdComplete()
				driverAction := channels.DriverAction{Action: "STOP_PROGRAM_EXECUTION"}
				channels.DriverActionChannel <- driverAction

			case "reset":
				logger.Trace("reset received — writing line 0 to disk before Reset()")
				// Permanently invalidate the active execution before Reset clears
				// StopExecution. Sleeping handlers retain the old RunID and exit.
				dt.ExecController.Abort()

				// Write 0 to disk BEFORE calling Reset().
				// Writing directly to disk here guarantees resolveStartLine()
				// will read 0 on the next Run regardless of asynchronous handler
				// completion. The Abort reason also prevents this cancellation
				// from being mistaken for a resumable operator Stop.
				//
				// clearLastLine/clearUserLine are defined in command_executor.go
				// as part of the adopted multi-file resume architecture.
				clearLastLine() // writes "0" to last_line.txt AND "" to last_program.txt
				clearUserLine() // discard any pending UI line selection

				execContextToModify.HasResetted = true
				execContextToModify.Reset()
				channels.NotifyCmdComplete()

			case "waiting_for_ecs":
				execContextToModify.WaitingForECS = true

			case "ecs_done":
				execContextToModify.WaitingForECS = false
			}
		}
	}(execContext)
}