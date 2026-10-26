package channels

import "fmt"
/*
This channel will be used to send input commands to command executors when in the middle of executing a program

For e.g. user can change the execution to single or continuous. Send emergency stop or stop execution of the program etc.
*/

// CommandExecInput holds the input data for the command executor
type CommandExecInput struct {
	InputType string
	Data      string
}

// CommandExecInputChannel channel used by command executor to alter the command execution pattern
var CommandExecInputChannel chan CommandExecInput

//WriteCommandExecInput writes the data to the CommandExecInputChannel
func WriteCommandExecInput(inputType string, data string) {
	cmdExecInput := CommandExecInput{InputType: inputType, Data: data}
	if CommandExecInputChannel == nil {
		fmt.Println("⚠️ CommandExecInputChannel is nil. Cannot send:", cmdExecInput)
		return
	}
	CommandExecInputChannel <- cmdExecInput
}
