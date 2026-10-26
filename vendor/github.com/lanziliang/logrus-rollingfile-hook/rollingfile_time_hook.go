package rollingfile

import (
	"github.com/sirupsen/logrus"
	"sort"
	"time"
)

type RollingFileTimeHook struct {
	*rollingFile

	timePattern string
	currentTimeFileName string
}

func NewRollingFileTimeHook(fpath, timePattern string, maxroll int) (*RollingFileTimeHook, error) {
	rf, err := newRollingFile(fpath, rollingTypeTime, maxroll)
	if err != nil {
		return nil, err
	}
	rfth := &RollingFileTimeHook{rf, timePattern, ""}
	rfth.self = rfth
	return rfth, nil
}

func(h *RollingFileTimeHook) Fire(e *logrus.Entry) error {
	h.rollLock.Lock()
	defer h.rollLock.Unlock()

	if h.needsToRoll() {
		if err := h.roll(); err != nil {
			return err
		}
	}

	// first time or rolling file
	if h.currentFile == nil {
		err := h.createFileAndFolderIfNeeded()
		if err != nil {
			return err
		}
		e.Logger.Out = h.currentFile
	}

	serialized, err := e.Logger.Formatter.Format(e)
	if err != nil {
		return err
	}
	h.currentFileSize += int64(len(serialized))

	return nil
}


func(h *RollingFileTimeHook) Levels() []logrus.Level {
	return logrus.AllLevels
}


func(h *RollingFileTimeHook) needsToRoll() bool {
	newName := time.Now().Format(h.timePattern)

	if h.currentTimeFileName == "" {
		// first run; capture the current name
		h.currentTimeFileName = newName
		return false
	}

	return newName != h.currentTimeFileName
}


func (h *RollingFileTimeHook) isFileRollNameValid(rname string) bool {
	if len(rname) == 0 {
		return false
	}
	_, err := time.ParseInLocation(h.timePattern, rname, time.Local)
	return err == nil
}


type rollTimeFileTailsSlice struct {
	data    []string
	pattern string
}

func (p rollTimeFileTailsSlice) Len() int {
	return len(p.data)
}

func (p rollTimeFileTailsSlice) Less(i, j int) bool {
	t1, _ := time.ParseInLocation(p.pattern, p.data[i], time.Local)
	t2, _ := time.ParseInLocation(p.pattern, p.data[j], time.Local)
	return t1.Before(t2)
}

func (p rollTimeFileTailsSlice) Swap(i, j int) {
	p.data[i], p.data[j] = p.data[j], p.data[i]
}


func (h *RollingFileTimeHook) sortFileRollNamesAsc(fs []string) ([]string, error) {
	ss := rollTimeFileTailsSlice{data: fs, pattern: h.timePattern}
	sort.Sort(ss)
	return ss.data, nil
}


func (h *RollingFileTimeHook) getNewHistoryRollFileName(_ []string) string {
	newFileName := h.currentTimeFileName
	h.currentTimeFileName = time.Now().Format(h.timePattern)
	return newFileName
}


func (h *RollingFileTimeHook) getCurrentFileName() string {
	return h.fileName
}