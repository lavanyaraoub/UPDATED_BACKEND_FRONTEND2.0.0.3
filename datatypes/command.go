package datatypes

import (
	"strconv"
	"strings"
)

//Data type which holds the command config defined in execution.yaml
//This stuct will say for a command which function to use etc.

//Command struct to hold commands which needs execution
type Command struct {
	Cmd                      string    `yaml:"cmd"`
	Func                     string    `yaml:"func"`
	Description              string    `yaml:"description"`
	DriveID                  int       `yaml:"driveId"`
	ConsiderInBlockExecution int       `yaml:"considerInBlockExecution"`
	Params                   []Command `yaml:"params"`
	CodeLineNumber           int       //this is the line number should send to the ui when the command is executing.
}

func (c *Command) GetCommandFirstChar() string {
	cmd := strings.ReplaceAll(c.Cmd, ";", "")
	val := string(cmd[0])
	return val
}

//GetValue returns the value accompanied by the command
//for e.g. A90 will return 90
func (c *Command) GetValue() string {
	cmd := strings.ReplaceAll(c.Cmd, ";", "")
	val := cmd[1:len(cmd)]
	return val
}

//GetValueAsInt returns the value accompanied by the command as int
//for e.g. A90 will return 90
func (c *Command) GetValueAsInt() (int, error) {
	cmd := strings.ReplaceAll(c.Cmd, ";", "")
	val := cmd[1:len(cmd)]
	intVal, err := strconv.Atoi(val)
	return intVal, err
}

//GetCommand based on passed command
func (e *Execution) GetCommand(passedCmd string) Command {
	passedCmd = strings.ToUpper(passedCmd)
	passedCmd = strings.ReplaceAll(passedCmd, ";", "")
	commandsSplitted := strings.Split(passedCmd, " ")
	var cmdToCheck string
	if len(commandsSplitted) <= 0 {
		cmdToCheck = passedCmd
	} else {
		cmdToCheck = commandsSplitted[0]
	}

	var invalidCommand Command
	for _, i := range e.Command {
		if i.Cmd == "INVALID" {
			invalidCommand = i
		}
		if commandMatch(i.Cmd, cmdToCheck) {
			return i
		}
		if isInMultiCommand(i.Cmd, cmdToCheck) {
			return i
		}
	}
	return invalidCommand
}

//Command in execution.yml can be configured like below
//G55|G53|G56, this function will split the configured command and check
//whether the passed command exist in the splitted one. If it exist then return true
//else false.
func isInMultiCommand(cmd string, cmdToCheck string) bool {
	splitted := strings.Split(cmd, "|")
	if len(splitted) <= 0 {
		return false
	}
	for _, i := range splitted {
		if commandMatch(i, cmdToCheck) {
			return true
		}
	}
	return false
}

//commandMatch checks whether the passed command matches with the configured command.
//This function also does the wildcard matching of command for e.g look for A** etc
func commandMatch(cmd string, cmdToCheck string) bool {
	//get the first char of the command for e.g. if the command is A90
	//then get A and append ** to it.
	withWildCard := string(cmdToCheck[0]) + "**"

	//compare for a full value for eg. A90 or wildcard value A**
	if cmd == cmdToCheck || cmd == withWildCard {
		return true
	}
	return false

}

//Execution type
type Execution struct {
	Command []Command
}

//YamlConfig root
type YamlConfig struct {
	Execution Execution
}
