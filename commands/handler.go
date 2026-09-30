package commands

import (
	dt "EtherCAT/datatypes"
)

//Handler interface to implement in all the execution function
type Handler interface {
	Handle(cmd dt.Command, execContext *dt.ExecutionContext) []dt.ExecutionResult
	CommandName() string
}
