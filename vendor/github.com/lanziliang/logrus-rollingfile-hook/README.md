# Rolling File Hook for [Logrus](https://github.com/sirupsen/logrus) 

Use this hook to writes received messages to a file, until time interval passes or file exceeds a specified limit. 
After that the current log file is renamed and writer starts to log into a new file. 
You can set a limit for such renamed files count, if you want, and then the hook would delete older ones when the files count exceed the specified limit.

The implementation of this hook refers to [Seelog](https://github.com/cihub/seelog) 

### Usage

```go
package main

import (
	"log/syslog"
	log "github.com/sirupsen/logrus"
	"github.com/lanziliang/logrus-rollingfile-hook"
)

func main() {
	hook, err := rollingfile.NewRollingFileTimeHook("./test.log", "2006-01-02", 5)
	if err != nil {
		panic(err)
	}
	defer hook.Close()
	
	log.AddHook(hook)
	log.Info("some logging message")
}
```

### TODO

-  Implement `RollingFileSizeHook`
