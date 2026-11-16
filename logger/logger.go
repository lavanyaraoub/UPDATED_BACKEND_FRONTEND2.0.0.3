package logger

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"EtherCAT/helper"
	settings "EtherCAT/settings"

	rollingfile "github.com/lanziliang/logrus-rollingfile-hook"
	"github.com/sirupsen/logrus"
)

var log = logrus.New()
var build = "debug"
var logEntry = logrus.NewEntry(log)

// Init initialize the logger and set the log level
func Init(logLevel string) {
	envSetting := settings.GetEnvSettings()
	build = envSetting.Mode

	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05.000",
	})

	// if the system is not running in debug then write logs to file
	if build != "debug" {
		hook, err := rollingfile.NewRollingFileTimeHook(helper.AppendWDPath("/log/log.log"), "2006-01-02", 5)
		if err != nil {
			panic(err)
		}
		defer hook.Close()
		log.AddHook(hook)
	} else {
		// running in debug mode write the logs to console
		log.SetOutput(os.Stdout)
	}

	switch logLevel {
	case "DEBUG":
		log.SetLevel(logrus.DebugLevel)
	case "INFO":
		log.SetLevel(logrus.InfoLevel)
	case "TRACE":
		log.SetLevel(logrus.TraceLevel)
	case "ERROR":
		log.SetLevel(logrus.ErrorLevel)
	default:
		log.SetLevel(logrus.DebugLevel)
	}

}

func logger() *logrus.Entry {
	if build == "debug" {
		pc, file, line, ok := runtime.Caller(2)
		if !ok {
			panic("Could not get context info for logger!")
		}

		filename := file[strings.LastIndex(file, "/")+1:] + ":" + strconv.Itoa(line)
		funcname := runtime.FuncForPC(pc).Name()
		fn := funcname[strings.LastIndex(funcname, ".")+1:]
		return log.WithField("fn", fn).WithField("f", filename)
	}
	return logEntry
}

// Info info logging
func Info(args ...interface{}) {
	logger().Info(args...)
}

// Error error logging
func Error(args ...interface{}) {
	logger().Error(args...)
}

// Warn warning logging
func Warn(args ...interface{}) {
	logger().Warn(args...)
}

// Debug debug logging
func Debug(args ...interface{}) {
	logger().Debug(args...)
}

// Trace log traces to understand whats going
func Trace(args ...interface{}) {
	logger().Trace(args...)
}

func Fatal(args ...interface{}) {
	logger().Fatal(args...)
}

// PrintStruct print struct with variable and value
func PrintStruct(args ...interface{}) {
	fmt.Printf("%+v\n", args)
}

// PrintOnSameLine repeat print on same line
func PrintOnSameLine(args ...interface{}) {
	//no same line printing in release mode
	if build == "debug" {
		fmt.Print("\033[G\033[K") // move the cursor left and clear the line
		fmt.Printf("%s\n", args)
		fmt.Print("\033[A") // move the cursor up
	}
}

// func createPretifier(f *runtime.Frame) (function string, file string) {
// 	pc, file, line, ok := runtime.Caller(6)
// 	if !ok {
// 		panic("Could not get context info for logger!")
// 	}

// 	filename := file[strings.LastIndex(file, "/")+1:] + ":" + strconv.Itoa(line)
// 	funcname := runtime.FuncForPC(pc).Name()
// 	fn := funcname[strings.LastIndex(funcname, ".")+1:]
// 	return fn, filename
// }
