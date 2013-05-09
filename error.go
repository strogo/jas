
package jas

import (
	"log"
	"errors"
	"fmt"
	"runtime"
	"bytes"
	"strings"
	"time"
)

//Default status code for request error, can be changed to the code you want.
var RequestErrorStatusCode = 400

//Default status code for internal error, can be changed to the code you want.
var InternalErrorStatusCode = 500

var UnauthorizedStatusCode = 401

var NotFoundStatusCode = 404

//Stack trace format which formats file name, line number and program counter.
var StackFormat = "%s:%d(0x%x);"

const (
	timeFormat = "02/Jan/2006:15:04:05 -0700"
	logFormat = "%v - %d [%v] \"%v %v %v\" 200 %d \"%v\" \"%v\"\n"
)

//If RequestError and internalError is not sufficient for you application,
//you can implement this interface to define your own error that can log itself in different way..
type AppError interface {

	//The actual error string that will be logged.
	Error() string

	//The status code that will be written to the response header.
	Status() int

	//The error message response to the client.
	//Can be the same string as Error() for request error
	//Should be simple string like "InternalError" for internal error.
	Message() string

	//Log self, it will be called after response is written to the client.
	//It runs on its own goroutine, so long running task will not affect the response time.
	Log(*Context)
}

type RequestError struct {
	Msg string
	StatusCode int
}
func (re RequestError) Error() string{
	return re.Msg
}
func (re RequestError) Status() int{
	return re.StatusCode
}

func (re RequestError) Message() string{
	return re.Msg
}

func (re RequestError) Log(context *Context){
	if context.config.RequestErrorLogger != nil {
		doLog(context.config.RequestErrorLogger, context, re, "-")
	}
}

type InternalError struct {
	Err error
	StatusCode int
}

func (ie InternalError) Status() int{
	return ie.StatusCode
}

func (ie InternalError) Error() string {
	return  ie.Err.Error()
}

func (ie InternalError) Message() string{
	return "InternalError"
}

func (ie InternalError) Log(context *Context){
	if context.config.InternalErrorLogger != nil {
		buf := new(bytes.Buffer)
		for i := 3; ; i++ {
			pc, file, line, ok := runtime.Caller(i)
			if !ok {
				break
			}
			suffix := file[len(file)-10:]
			if suffix == "me/panic.c" {
				continue
			}
			if suffix == "t/value.go" {
				break
			}
			fmt.Fprintf(buf, StackFormat, file, line, pc)
		}
		doLog(context.config.InternalErrorLogger, context, ie, buf.String())
	}
}

func doLog(logger *log.Logger, context *Context, err error, stack string){
	errStr := err.Error()
	errStr = strings.Replace(errStr, "\n", ";", -1)
	logger.Printf(
		logFormat,
		context.RemoteAddr,
		context.UserId,
		time.Now().Format(timeFormat),
		context.Method,
		context.RequestURI,
		context.Proto,
		context.written,
		errStr,
		stack,
	)
}

//Request error's message will be sent to the client.
func NewRequestError(message string) RequestError{
	return RequestError{message, RequestErrorStatusCode}
}

func NewInternalError(err interface {}) InternalError{
	e, ok := err.(error);
	if !ok {
		e = errors.New(fmt.Sprint(err))
	}
	return InternalError{e, InternalErrorStatusCode}
}