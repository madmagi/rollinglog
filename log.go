// Copyright 2013 Matthew Endsley
// All rights reserved
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted providing that the following conditions
// are met:
// 1. Redistributions of source code must retain the above copyright
//    notice, this list of conditions and the following disclaimer.
// 2. Redistributions in binary form must reproduce the above copyright
//    notice, this list of conditions and the following disclaimer in the
//    documentation and/or other materials provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE AUTHOR ``AS IS'' AND ANY EXPRESS OR
// IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
// WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
// ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY
// DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
// DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
// OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
// HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
// STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING
// IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
// POSSIBILITY OF SUCH DAMAGE.

package rollinglog

import (
	"io"
	"log"
	"os"
	"path"
	"regexp"
	"syscall"
	"time"
)

const (
	FlagCaptureStdout = 1 << iota
	FlagCaptureStderr
)

var (
	pattern = regexp.MustCompile("{.*}")
)

type Config struct {
	FilepathPattern string
	Mode            os.FileMode
	DirMode         os.FileMode
	Flags           uint
}

func NewMust(config Config) io.WriteCloser {
	wc, err := New(config)
	if err != nil {
		log.Panic("Failed to create log: ", err)
	}
	return wc
}

// Create a new io.WriteCloser that targets a rolling log file. Uses path as a
// template, adding the current date.
//		data/server.log becomes data/2006/01/2006-01-02/server.log
func New(config Config) (io.WriteCloser, error) {
	if config.FilepathPattern == "" {
		config.FilepathPattern = "logs/{2006/01/2006-01-02}/log.log"
	}
	if config.Mode == 0 {
		config.Mode = 0600
	}
	if config.DirMode == 0 {
		config.DirMode = 02700
	}

	chFile := make(chan *os.File)
	chErr := make(chan error)
	chClosed := make(chan struct{})

	go func() {
		defer close(chFile)
		for {
			select {
			case <-chClosed:
				return
			default:
			}

			now := time.Now()
			p := pattern.ReplaceAllStringFunc(config.FilepathPattern, func(s string) string {
				return now.Format(s[1 : len(s)-1])
			})
			if err := os.MkdirAll(path.Dir(p), config.DirMode); err != nil && !os.IsExist(err) {
				select {
				case chErr <- err:
				case <-chClosed:
				}
				return
			}

			f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, config.Mode)
			if err != nil {
				select {
				case chErr <- err:
				case <-chClosed:
				}
				return
			}

			if config.Flags&FlagCaptureStdout != 0 {
				fd, stdout := int(f.Fd()), int(os.Stdout.Fd())
				syscall.Close(stdout)
				syscall.Dup2(fd, stdout)
			}
			if config.Flags&FlagCaptureStderr != 0 {
				fd, stderr := int(f.Fd()), int(os.Stderr.Fd())
				syscall.Close(stderr)
				syscall.Dup2(fd, stderr)
			}

			// wait for tomorrow
			tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())

			timeout := time.After(tomorrow.Sub(now))
			select {
			case chFile <- f:
				// wait for timeout
				<-timeout
			case <-chClosed:
				return
			case <-timeout:
				f.Close()
			}
		}
	}()

	var f *os.File
	select {
	case f = <-chFile:
	case err := <-chErr:
		return nil, err
	}

	return &rollingFile{
		f:        f,
		chFile:   chFile,
		chErr:    chErr,
		chClosed: chClosed,
	}, nil
}

type rollingFile struct {
	f        *os.File
	lastErr  error
	chFile   <-chan *os.File
	chErr    <-chan error
	chClosed chan<- struct{}
}

func (rf *rollingFile) Write(p []byte) (int, error) {
	select {
	case rf.lastErr = <-rf.chErr:
	case f := <-rf.chFile:
		rf.f.Close()
		rf.f = f
	default:
	}

	if rf.lastErr != nil {
		return 0, rf.lastErr
	}

	return rf.f.Write(p)
}

func (rf *rollingFile) Close() error {
	if rf.f != nil {
		rf.f.Close()
	}
	rf.lastErr = io.EOF
	close(rf.chClosed)
	return nil
}
