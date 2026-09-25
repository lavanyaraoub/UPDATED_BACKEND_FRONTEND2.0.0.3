package licensechecker

import (
	"EtherCAT/logger"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
)

//ExceptionMessage struc to hold exception message
type ExceptionMessage struct {
	Message string `json:"message"`
}

//HasError checks for any error in the resonse
func HasError(resp *http.Response) error {
	if resp.StatusCode != 200 {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		var ex ExceptionMessage
		json.Unmarshal(bodyBytes, &ex)
		logger.Error("ERROR: " + ex.Message)
		return errors.New(ex.Message)
	}
	return nil
}
