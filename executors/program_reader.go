package executors

import (
	dt "EtherCAT/datatypes"
	"bufio"
	"errors"
	"os"
	"strings"
)

func ParserCommand(fileName string) ([]dt.Command, error) {
	return createCommands(fileName)
}

//createCommands read the passed file and returns them as command type array
//which can be executed by the command handlers
func createCommands(fileName string) ([]dt.Command, error) {
	commandLines, err := readCommandsFile(fileName)
	if err != nil {
		return nil, err
	}
	commandsToExec := []dt.Command{}
	for i, cmdlin := range commandLines {
		//empty space, ignore line
		if len(cmdlin) <= 0 {
			continue
		}

		//should be comment and no need to take into consideration, ignore line
		if strings.HasPrefix(cmdlin, "#") || strings.HasPrefix(cmdlin, "/") {
			continue
		}
		cmdlin, err = extractCommand(cmdlin)
		if err != nil {
			return nil, err
		}
		if len(cmdlin) > 0 {
			command := dt.Command{Cmd: strings.ToUpper(cmdlin), CodeLineNumber: i}
			commandsToExec = append(commandsToExec, command)
		}
	}
	return commandsToExec, nil
}

//readCommandsFile read command file line by line and return as a string array
func readCommandsFile(fileName string) ([]string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)
	var txtlines []string

	for scanner.Scan() {
		txtlines = append(txtlines, scanner.Text())
	}

	return txtlines, nil
}

//extractCommand extract the command. Remove semi colon and any strings after the semicolon.
//Anything after semicolon is considered as comment
func extractCommand(cmdLine string) (string, error) {
	if idx := strings.Index(cmdLine, ";"); idx != -1 {
		return cmdLine[:idx], nil
	}
	//based on the requirement, if the code is not ending with ; then should throw
	//syntax error back to ui.
	return "", errors.New("Syntax error")
}