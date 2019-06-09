// Copyright 2019 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package withstack

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors/errbase"
	raven "github.com/getsentry/raven-go"
	pkgErr "github.com/pkg/errors"
)

// ReportableStackTrace aliases the type of the same name in the raven
// (Sentry) package. This is used by the 'report' error package.
type ReportableStackTrace = raven.Stacktrace

// GetReportableStackTrace extracts a stack trace embedded in the
// given error in the format suitable for raven/Sentry reporting.
//
// This supports:
// - errors generated by github.com/pkg/errors (either generated
//   locally or after transfer through the network),
// - errors generated with WithStack() in this package,
// - any other error that implements a StackTrace() method
//   returning a StackTrace from github.com/pkg/errors.
//
// Note: Sentry wants the oldest call frame first, so
// the entries are reversed in the result.
func GetReportableStackTrace(err error) *ReportableStackTrace {
	// If we have a stack trace in the style of github.com/pkg/errors
	// (either from there or our own withStack), use it.
	if st, ok := err.(interface{ StackTrace() pkgErr.StackTrace }); ok {
		return convertPkgStack(st.StackTrace())
	}

	// If we have flattened a github.com/pkg/errors-style stack
	// trace to a string, it will happen in the error's safe details
	// and we need to parse it.
	if sd, ok := err.(errbase.SafeDetailer); ok {
		details := sd.SafeDetails()
		if len(details) > 0 {
			switch errbase.GetTypeKey(err) {
			case pkgFundamental, pkgWithStackName, ourWithStackName:
				return parsePrintedStack(details[0])
			}
		}
	}

	// No conversion available - no stack trace.
	return nil
}

type frame = raven.StacktraceFrame

// convertPkgStack converts a StackTrace from github.com/pkg/errors
// to a Stacktrace in github.com/getsentry/raven-go.
func convertPkgStack(st pkgErr.StackTrace) *ReportableStackTrace {
	// If there are no frames, the entire stacktrace is nil.
	if len(st) == 0 {
		return nil
	}

	// Note: the stack trace logic changed between go 1.11 and 1.12.
	// Trying to analyze the frame PCs point-wise will cause
	// the output to change between the go versions.
	return parsePrintedStack(fmt.Sprintf("%+v", st))
}

// getSourceInfoFromPc extracts the details for a given program counter.
func getSourceInfoFromPc(pc uintptr) (file string, line int, fn *runtime.Func) {
	fn = runtime.FuncForPC(pc)
	if fn != nil {
		file, line = fn.FileLine(pc)
	} else {
		file = "unknown"
	}
	return file, line, fn
}

// trimPath is a copy of the same function in package raven-go.
func trimPath(filename string) string {
	const src = "/src/"
	prefix := strings.LastIndex(filename, src)
	if prefix == -1 {
		return filename
	}
	return filename[prefix+len(src):]
}

// functionName is an adapted copy of the same function in package raven-go.
func functionName(fnName string) (pack string, name string) {
	name = fnName
	if idx := strings.LastIndex(name, "."); idx != -1 {
		pack = name[:idx]
		name = name[idx+1:]
	}
	name = strings.Replace(name, "·", ".", -1)
	return
}

// parsePrintedStack reverse-engineers a reportable stack trace from
// the result of printing a github.com/pkg/errors stack trace with format %+v.
func parsePrintedStack(st string) *ReportableStackTrace {
	// A printed stack trace looks like a repetition of either:
	// "unknown"
	// or
	// <result of fn.Name()>
	// <tab><file>:<linenum>
	// It's also likely to contain a heading newline character(s).
	var frames []*frame
	lines := strings.Split(strings.TrimSpace(st), "\n")
	for i := 0; i < len(lines); i++ {
		nextI, file, line, fnName := parsePrintedStackEntry(lines, i)
		i = nextI

		// Compose the frame.
		frame := &frame{
			AbsolutePath: file,
			Filename:     trimPath(file),
			Lineno:       line,
			InApp:        false,
			Module:       "unknown",
			Function:     fnName,
		}
		if fnName != "unknown" {
			// Extract the function/module details.
			frame.Module, frame.Function = functionName(fnName)
		}
		frames = append(frames, frame)
	}

	if frames == nil {
		return nil
	}

	// Sentry wants the frames with the oldest first, so reverse them.
	for i, j := 0, len(frames)-1; i < j; i, j = i+1, j-1 {
		frames[i], frames[j] = frames[j], frames[i]
	}

	return &ReportableStackTrace{Frames: frames}
}

// parsePrintedStackEntry extracts the stack entry information
// in lines at position i. It returns the new value of i if more than
// one line was read.
func parsePrintedStackEntry(
	lines []string, i int,
) (newI int, file string, line int, fnName string) {
	// The function name is on the first line.
	fnName = lines[i]

	// The file:line pair may be on the line after that.
	if i < len(lines)-1 && strings.HasPrefix(lines[i+1], "\t") {
		fileLine := strings.TrimSpace(lines[i+1])
		// Separate file path and line number.
		lineSep := strings.LastIndexByte(fileLine, ':')
		if lineSep == -1 {
			file = fileLine
		} else {
			file = fileLine[:lineSep]
			lineStr := fileLine[lineSep+1:]
			line, _ = strconv.Atoi(lineStr)
		}
		i++
	}
	return i, file, line, fnName
}

var pkgFundamental errbase.TypeKey
var pkgWithStackName errbase.TypeKey
var ourWithStackName errbase.TypeKey

func init() {
	err := errors.New("")
	pkgFundamental = errbase.GetTypeKey(pkgErr.New(""))
	pkgWithStackName = errbase.GetTypeKey(pkgErr.WithStack(err))
	ourWithStackName = errbase.GetTypeKey(WithStack(err))
}
