/*
A resource is a struct pointer with one or more exported methods
that accepts only one parameter of type *jas.Context, The resource name and method
name will be converted to snake case by default.(can be changed in config).

If resource type name ends with "Id", an integer path segment is added between the resource name
and the method name.

If resource method name ends with "Id", an integer path segment is added to the handled path after the method name.

You can get the Id value directly in *jas.Context field.

If resource implements ResourceWithGap interface, the handled path will has gap segments between resource name and method name.

HTTP "POST", "PUT", "DELETE" will be routed to resource methods with "Post", "Put", "Delete" prefix.
HTTP "GET" will be routed to resource methods with or without "Get" prefix.
Other HTTP request with methods like "HEAD", "OPTIONS" will be routed to resource "Get" method.

Examples:

GET "/users/image_url":

    func (u *Users) ImageUrl (ctx *jas.Context) {}

GET "/users/5/image_url":

    func (ui *UsersId) ImageUrl (ctx *jas.Context) {}

GET "/image_url":

    func (*UsersOmit) ImageUrl (ctx *jas.Context) {}

POST "/users/image_url":

    func (*Users) PostImageUrl (ctx *jas.Context) {}

HEAD "/users/image_url":

    func (*Users) ImageUrl (ctx *jas.Context {}

*/
package jas

import (
	"net/http"
	"reflect"
	"strings"
	"log"
	"os"
	"compress/gzip"
	"sort"
	"encoding/json"
	"bytes"
	"io"
	"strconv"
)

var WordSeparator = "_"

type Router struct {
	methodMap map[string]reflect.Value
	gapsMap   map[string][]string
	*Config
}

type ResourceWithGap interface {
	Gap() string
}

type Config struct {
	//The base path of the request url.
	//If you want to make it work along with other router or http.Handler,
	//it can be used as a pattern string for `http.Handle` method
	//It must starts and ends with "/", e.g. "/api/v1/".
	//Defaults to "/".
	BasePath string

	//Handle Cross-origin Resource Sharing.
	//It accept request and response header parameter.
	//return true to go on handle the request, return false to stop handling and response with header only.
	//Defaults to nil
	//You can set it to AllowCORS function to allow all CORS request.
	HandleCORS func (*http.Request, http.Header) bool

	//gzip is disabled by default. set true to enable it
	EnableGzip bool

	//defaults to nil, if set, request error will be logged.
	RequestErrorLogger *log.Logger

	//log to standard err by default.
	InternalErrorLogger *log.Logger

	//If set, it will be called after recovered from panic.
	//Do time consuming work in the function will not increase response time because it runs in its own goroutine.
	OnAppError func (AppError, *Context)

	//If set, it will be called before calling the matched method.
	BeforeServe func (*Context)

	//If set, the user id can be obtained by *Context.UserId and will be logged on error.
	//Implementations can be like decode cookie value or token parameter.
	ParseIdFunc func (*http.Request) int64

	//If set, the delimiter will be appended to the end of the data on every call to *Context.FlushData method.
	FlushDelimiter []byte

	//handler function for unhandled path request.
	//default function just send `{"data":null,"error":"NotFound"}` with 404 status code.
	OnNotFound func (http.ResponseWriter, *http.Request)

	//if you do not like the default json format `{"data":...,"error":...}`,
	//you can define your own write function here.
	//The io.Writer may be http.ResponseWriter or GzipWriter depends on if gzip is enabled.
	//The errMessage is of type string or nil, it's not AppError.
	//it should return the number of bytes has been written.
	HijackWrite func (io.Writer, *Context) int
}

//Implements http.Handler interface.
func (router *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, router.BasePath) {
		router.OnNotFound(w, r)
		return
	}
	rawPath := r.URL.Path[len(router.BasePath):]
	path, id, segments, gaps := router.resolvePath(r.Method, rawPath)
	methodValue, ok := router.methodMap[path]
	if !ok {
		router.OnNotFound(w, r)
		return
	}
	ctx := new(Context)
	ctx.Id = id
	ctx.pathSegments = segments
	ctx.Request = r
	ctx.gaps = gaps
	ctx.Finder = FinderWithRequest(r)
	ctx.ResponseHeader = w.Header()
	ctx.config = router.Config
	ctx.responseWriter = w
	ctx.Status = 200
	if router.HandleCORS != nil && !router.HandleCORS(r, ctx.ResponseHeader) {
		return
	}
	if router.EnableGzip && strings.Contains(ctx.Header.Get("Accept-Encoding"), "gzip") {
		gz := gzip.NewWriter(ctx.responseWriter)
		defer gz.Close()
		ctx.ResponseHeader.Set("Content-Encoding", "gzip")
		ctx.writer = gz
	}else {
		ctx.writer = ctx.responseWriter
	}
	if router.ParseIdFunc != nil {
		ctx.UserId = router.ParseIdFunc(r)
	}
	ctx.ResponseHeader.Set("Cache-Control", "no-cache")
	ctx.ResponseHeader.Set("Content-Type", "application/json; charset=utf-8")
	defer ctx.deferredResponse()
	if router.BeforeServe != nil {
		router.BeforeServe(ctx)
	}
	methodValue.Call([]reflect.Value {reflect.ValueOf(ctx)})
}

//Get the paths that have been handled by resources.
//The paths are sorted, it can be used to detect api path changes.
func (r *Router) HandledPaths(withBasePath bool) string {
	var handledPaths []string
	basePath := ""
	if withBasePath {
		basePath = strings.TrimSuffix(r.BasePath, "/")
	}
	for k, _ := range r.methodMap {
		methodPath := strings.Split(k, " ")
		handeldPath := methodPath[0]+ " " + basePath + methodPath[1]
		handledPaths = append(handledPaths, handeldPath)
	}
	sort.Strings(handledPaths)
	return strings.Join(handledPaths, "\n")
}

// Construct a Router instance.
// Then you can set the configuration fields to config the router.
// Configuration fields applies to a single router, there are also some package level variables
// you can change if needed.
// You can make multiple routers with different base path to handle requests to the same host.
// See documentation about resources at the top of the file.
func NewRouter(resources ...interface {}) *Router {
	router := new(Router)
	router.methodMap = map[string]reflect.Value{}
	router.gapsMap = map[string][]string{}
	config := new(Config)
	config.BasePath = "/"
	config.InternalErrorLogger = log.New(os.Stderr, "", 0)
	config.OnNotFound = notFound
	router.Config = config
	for _, v := range resources {
		resType := reflect.TypeOf(v)
		resValue := reflect.ValueOf(v)
		resName := resType.Elem().Name()
		resNameSnake := convertName(resName)
		resNameSnakeLen := len(resNameSnake)
		var isIdResource bool
		var gap string
		if resNameSnakeLen > 3 && resNameSnake[resNameSnakeLen - 3:] == "_id" {
			resNameSnake = resNameSnake[:resNameSnakeLen - 3]
			resNameSnake += "/:id"
			isIdResource = true
		}else if resWithGap, ok := v.(ResourceWithGap); ok{
			gap = resWithGap.Gap()
			router.gapsMap[resNameSnake] = strings.Split(gap, "/")
			resNameSnake += "/" + gap
		}
		for i := 0; i < resType.NumMethod(); i++ {
			methodType := resType.Method(i)
			if !validateMethod(&methodType) {
				continue
			}
			httpMethod := "GET"
			methodName := convertName(methodType.Name)
			methodWords := strings.Split(methodName, WordSeparator)
			var hasHttpMethod bool
			minIdMethodLen := 2
			switch methodWords[0]{
			case "post", "get", "put", "delete":
				hasHttpMethod = true
				minIdMethodLen++
			}
			var isIdMethod bool
			if !isIdResource && len(methodWords) >= minIdMethodLen && methodWords[len(methodWords) - 1] == "id" {
				methodName = methodName[:len(methodName) - 3]
				isIdMethod = true
			}
			if hasHttpMethod {
				if len(methodWords) > 1 {
					methodName = "/" + methodName[len(methodWords[0]) + 1:]
				}else {
					methodName = ""
				}
				httpMethod = strings.ToUpper(methodWords[0])
			}else {
				methodName = "/" + methodName
			}
			if isIdMethod {
				methodName += "/:id"
			}
			methodValue := resValue.Method(i)
			if resNameSnake == "" && len(methodName) > 0 {
				methodName = methodName[1:]
			}
			path := httpMethod + " /" + resNameSnake + methodName
			router.methodMap[path] = methodValue
		}
	}
	return router
}

var contextType = reflect.TypeOf(new(Context))

func validateMethod(method *reflect.Method) bool {
	firstLetter := method.Name[0]
	if firstLetter < 'A' || firstLetter > 'Z' {
		return false
	}
	if method.Type.NumIn() != 2 {
		return false
	}
	if method.Type.NumOut() != 0 {
		return false
	}
	inType := method.Type.In(1)
	if inType != contextType {
		return false
	}
	return true
}

func convertName(name string) string {
	buf := bytes.NewBufferString("")
	for i, v := range name {
		if i > 0 && v >= 'A' && v <= 'Z' {
			buf.WriteString(WordSeparator)
		}
		buf.WriteRune(v)
	}
	return strings.ToLower(buf.String())
}

func notFound(w http.ResponseWriter, r *http.Request) {
	var response Response
	response.Error = "NotFound"
	jsonbytes, _ := json.Marshal(response)
	w.WriteHeader(NotFoundStatusCode)
	w.Write(jsonbytes)
}

//This is an implementation of HandleCORS function to allow all cross domain request.
func AllowCORS(r *http.Request, responseHeader http.Header) bool {
	responseHeader.Add("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		return false
	}
	return true
}

func (r *Router) resolvePath(method string, rawPath string) (path string, id int64, segments []string, gaps []string) {
	segments = strings.Split(rawPath, "/")
	httpMethod := "GET"
	switch method{
	case "POST", "DETETE", "PUT":
		httpMethod = method
	}
	path = httpMethod + " /" + segments[0]
	seg1 := ""
	if len(segments) >= 2 {
		seg1 = segments[1]
	}
	id, err := strconv.ParseInt(seg1, 10, 64)
	if err == nil {
		path += "/:id"
		if len(segments) > 2 && segments[2] != "" {
			path += "/" + segments[2]
		}
	}else{
		gaps = r.gapsMap[segments[0]]
		if gaps != nil {
			path += "/" + strings.Join(gaps, "/")
		}
		methodIndex := len(gaps) + 1
		if len(segments) > methodIndex && segments[methodIndex] != "" {
			path += "/" + segments[methodIndex]
		}
		nextIndex := methodIndex + 1
		if len(segments) > nextIndex && segments[nextIndex] != "" {
			id, err = strconv.ParseInt(segments[nextIndex], 10, 64)
			if err == nil {
				path += "/:id"
			}
		}
	}
	return
}
