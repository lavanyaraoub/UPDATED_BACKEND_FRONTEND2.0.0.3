package settings

import (
    "encoding/json"
    "io/ioutil"
   
)

type UserLineSettings struct {
    UserLine string `json:"user_line"`
}

var userLineFile = "/mnt/app/jamun/settings/userline.json"

// SaveLineNumber saves the user line into a separate JSON file
func SaveLineNumber(line string) error {
    s := UserLineSettings{UserLine: line}
    data, err := json.MarshalIndent(s, "", "  ")
    if err != nil {
        return err
    }
    return ioutil.WriteFile(userLineFile, data, 0644)
}

// LoadLineNumber loads the user line back from file
func LoadLineNumber() (string, error) {
    data, err := ioutil.ReadFile(userLineFile)
    if err != nil {
        return "", err
    }
    var s UserLineSettings
    if err := json.Unmarshal(data, &s); err != nil {
        return "", err
    }
    return s.UserLine, nil
}
