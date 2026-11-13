package datatypes

//ExecutionResult return struc from each function
type ExecutionResult struct {
	Description   string
	Cmd           Command
	ShouldExecute bool
}
