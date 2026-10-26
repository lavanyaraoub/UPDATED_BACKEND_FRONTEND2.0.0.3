package channels

//CommandExecStatus communicate to the command handler about the
//completion of execution via this struct. The command handler will wait for this
//message via channel
type CommandExecStatus struct {
	Completed bool
}

//CommandExecStatusChannel required command handlers listen to this channel
//and quit the command when it receives Completed signal from motor driver.
var CommandExecStatusChannel chan CommandExecStatus

var SingleModeChannel chan CommandExecStatus

var isChannelOpen bool
var isSingleModeOpen bool

func init() {
	CommandExecStatusChannel = make(chan CommandExecStatus, 1)
	SingleModeChannel = make(chan CommandExecStatus)
}

//OpenWaitChannel open the channel that commands are waiting. If the channel is open in WaitTillCmdComplete
//there is a chance that caller might send data back before the channel is open and cause errors.
func OpenWaitChannel() {
	// if CommandExecStatusChannel != nil {
	// 	close(CommandExecStatusChannel)
	// 	CommandExecStatusChannel = nil
	// }

	isChannelOpen = true
}

//OpenSingleModeChannel set as single block command exec mode
func OpenSingleModeChannel() {
	isSingleModeOpen = true
}

//WaitTillCmdComplete command hanlders can call this function and will keep them
//wait until the motor driver completes the desired action.
func WaitTillCmdComplete() {
	if !isChannelOpen {
		return
	}
	// for {
	// 	msg := <-CommandExecStatusChannel
	// 	if msg.Completed == true {
	// 		break
	// 	}
	// }
	<-CommandExecStatusChannel
	isChannelOpen = false
}

//WaitTillSingleModeComplete wait for the command from ui to proceed to next command
func WaitTillSingleModeComplete() {
	if !isSingleModeOpen {
		return
	}
	<-SingleModeChannel
	isSingleModeOpen = false
}

//NotifyCmdComplete motor drivers can call this function to notify command handlers
//that the execution of the command is now complete
func NotifyCmdComplete() {
	if isChannelOpen {
		msg := CommandExecStatus{Completed: true}
		CommandExecStatusChannel <- msg
	}
	isChannelOpen = false
}

//NotifySingleModeComplete ui notified to move to next command
func NotifySingleModeComplete() {
	if isSingleModeOpen {
		msg := CommandExecStatus{Completed: true}
		SingleModeChannel <- msg
	}
}
