package executors

import (
	h "EtherCAT/commands"
	"EtherCAT/helper"
	logger "EtherCAT/logger"
	"io/ioutil"
	"os"
	"plugin"
	"strings"
)

//function checks whether a file is a plugin. If the file is a directory then not a plugin
//a go file then not a plugin. Any file contains .so is a valid plugin
func notAPlugin(file os.FileInfo) bool {
	if file.IsDir() {
		return true
	}

	if !strings.Contains(file.Name(), ".so") {
		return true
	}
	return false
}

//loadCommandPlugins load all command plugins from plugins folder
func loadCommandPlugins() (map[string]h.Handler, error) {
	commandMap := make(map[string]h.Handler)
	logger.Info("Loading command plugins ...")
	files, _ := ioutil.ReadDir(helper.AppendWDPath("/commands"))
	for _, file := range files {
		if notAPlugin(file) {
			continue
		}

		//open the plugins from plugin folder.
		plug, err := plugin.Open(helper.AppendWDPath("/commands/" + file.Name()))
		if err != nil {
			logger.Error(err)
			return nil, err
		}
		commandHandler, err := plug.Lookup("Command")
		if err != nil {
			logger.Error(err)
			return nil, err
		}

		var handler h.Handler
		handler, ok := commandHandler.(h.Handler)
		if !ok {
			logger.Error("unexpected type from module symbol")
			continue
		}
		commandName := handler.CommandName()
		commandMap[commandName] = handler
		logger.Debug("Loaded", commandName, "plugin from file", file.Name())
	}
	return commandMap, nil
}
