/*-
 * Copyright (c) 2020, Jörg Pernfuß
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

// Package lhm implements a loghandle map to manage multiple loggers
// with file reopen support.
package lhm // import "github.com/mjolnir42/lhm"

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/client9/reopen"
	"github.com/sirupsen/logrus"
)

// LogHandleMap is a concurrent map that is used to look up
// filehandles of active logfiles
type LogHandleMap struct {
	hmap map[string]*reopen.FileWriter
	lmap map[string]*logrus.Logger
	sync.RWMutex
	bp     string
	signal chan os.Signal
}

// New returns an initialized LogHandleMap
func New(basepath string) (*LogHandleMap, *chan os.Signal) {
	lm := &LogHandleMap{}
	lm.hmap = make(map[string]*reopen.FileWriter)
	lm.lmap = make(map[string]*logrus.Logger)
	lm.bp = basepath

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGUSR2)
	lm.signal = sc
	return lm, &sc
}

// Add registers a new filehandle
func (x *LogHandleMap) Add(key string, fh *reopen.FileWriter, lg *logrus.Logger) {
	x.Lock()
	defer x.Unlock()
	x.hmap[key] = fh
	x.lmap[key] = lg
}

// GetFileHandle retrieves a filehandle
func (x *LogHandleMap) GetFileHandle(key string) *reopen.FileWriter {
	x.RLock()
	defer x.RUnlock()
	return x.hmap[key]
}

// GetLogger retrieves a logger
func (x *LogHandleMap) GetLogger(key string) *logrus.Logger {
	x.Lock()
	defer x.Unlock()
	return x.lmap[key]
}

// Del removes a filehandle
func (x *LogHandleMap) Del(key string) {
	x.Lock()
	defer x.Unlock()
	delete(x.hmap, key)
}

// Open creates a new logger with registration name fname, backed by
// fname.log at the registered basepath
func (x *LogHandleMap) Open(fname string, lvl logrus.Level) (err error) {
	// attempt to move existing files (includes various race conditions)
	_ = os.Rename(
		filepath.Join(x.bp, fname+`.log`),
		filepath.Join(x.bp, fname+`.log.`+time.Now().UTC().Format(time.RFC3339)),
	)

	//
	var fh *reopen.FileWriter
	if fh, err = reopen.NewFileWriter(
		filepath.Join(x.bp, fname+`.log`),
	); err != nil {
		return
	}
	nl := logrus.New()
	nl.Out = fh
	nl.Formatter = &logrus.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	}
	nl.Infoln(fmt.Sprintf("Started logfile `%s` at %s",
		fname,
		time.Now().UTC().Format(time.RFC3339),
	))
	nl.SetLevel(lvl)
	x.Add(fname, fh, nl)
	return
}

// getLoggerNolock retrieves a Logger without locking. This should be used
// inside an active Range() lock.
func (x *LogHandleMap) getLoggerNolock(key string) *logrus.Logger {
	return x.lmap[key]
}

// rangeLock locks l and returns the embedded map. Unlocking must
// be done by the caller via rangeUnlock()
func (x *LogHandleMap) rangeLock() map[string]*reopen.FileWriter {
	x.Lock()
	return x.hmap
}

// rangeUnlock unlocks l. It is required to be called after rangeLock() once
// the caller is finished with the map.
func (x *LogHandleMap) rangeUnlock() {
	x.Unlock()
}

// Reopen should be called inside a go-routine. Inside is an infinite
// loop that waits for a signal delivered via the channel returned by
// New().
// Whenever a signal is received, it cycles through all registered logfile
// handles and reopens them, unless their registration names starts with
// ignorePrefix. If a reopen operation fails, the error is passed to
// abortFunc and no further handles are reopened.
func (x *LogHandleMap) Reopen(ignorePrefix string, abortFunc func(e error)) {
	for {
		select {
		case <-x.signal:
			locked := true
		fileloop:
			for name, lfHandle := range x.rangeLock() {
				if strings.HasPrefix(name, ignorePrefix) {
					continue
				}

				// reopen logfile handle
				err := lfHandle.Reopen()

				if err != nil {
					x.rangeUnlock()
					locked = false
					abortFunc(err)

					break fileloop
				}

				// get logger for associated filehandle
				lg := x.getLoggerNolock(name)
				// store configured filter level
				lvl := lg.Level
				// write out logrotate information marker
				lg.SetLevel(logrus.InfoLevel)
				lg.Infoln(fmt.Sprintf("Reopened logfile `%s` for logrotate at %s",
					name,
					time.Now().UTC().Format(time.RFC3339),
				))
				// restore configured filter level
				lg.SetLevel(lvl)
			}
			if locked {
				x.rangeUnlock()
			}
		}
	}
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
