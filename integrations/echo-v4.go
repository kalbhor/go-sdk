package integrations

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/keploy/go-sdk/keploy"
	"github.com/labstack/echo/v4"
)

// WebGoV4 adds middleware for API testing into webgo router. 
// app parameter is app instance and w parameter is webgo v4 router of your API 
func EchoV4(app *keploy.App, e *echo.Echo) {
	mode := os.Getenv("KEPLOY_SDK_MODE")
	switch mode {
	case "test":
		e.Use(NewMiddlewareContextValue)
		e.Use(testMW(app))
		go app.Test()
	case "off":
		// dont run the SDK
	case "capture":
		e.Use(NewMiddlewareContextValue)
		e.Use(captureMW(app))
	}
}

func testMW(app *keploy.App) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == app {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			id := c.Request().Header.Get("KEPLOY_TEST_ID")
			if id == "" {
				return next(c)
			}
			tc := app.Get(id)
			if tc == nil {
				return next(c)
			}
			c.Set(string(keploy.KCTX), &keploy.Context{
				Mode:   "test",
				TestID: id,
				Deps:   tc.Deps,
			})
			resp := captureResp(c, next)
			app.Resp[id] = resp
			return
		}
	}
}

func captureMW(app *keploy.App) func(echo.HandlerFunc) echo.HandlerFunc {
	if nil == app {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			c.Set(string(keploy.KCTX), &keploy.Context{
				Mode: "capture",
			})
			id := c.Request().Header.Get("KEPLOY_TEST_ID")
			if id != "" {
				// id is only present during simulation
				// run it similar to how testcases would run
				c.Set(string(keploy.KCTX), &keploy.Context{
					Mode:   "test",
					TestID: id,
					Deps:   app.Deps[id],
				})
				resp := captureResp(c, next)
				app.Resp[id] = resp
				return
			}

			// Request
			var reqBody []byte
			if c.Request().Body != nil { // Read
				reqBody, err = ioutil.ReadAll(c.Request().Body)
				if err != nil {
					// TODO right way to log errors
					return
				}
			}
			c.Request().Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset

			// Response
			resp := captureResp(c, next)

			d := c.Request().Context().Value(keploy.KCTX)
			if d == nil {
				app.Log.Error("failed to get keploy context")
				return

			}
			deps := d.(*keploy.Context)

			//u := &url.URL{
			//	Scheme:   c.Scheme(),
			//	//User:     url.UserPassword("me", "pass"),
			//	Host:     c.Request().Host,
			//	Path:     c.Request().URL.Path,
			//	RawQuery: c.Request().URL.RawQuery,
			//}

			app.Capture(keploy.TestCaseReq{
				Captured: time.Now().Unix(),
				AppID:    app.Name,
				URI:      urlPathEcho(c.Request().URL.Path, pathParamsEcho(c)) ,
				HttpReq: keploy.HttpReq{
					Method:     keploy.Method(c.Request().Method),
					ProtoMajor: c.Request().ProtoMajor,
					ProtoMinor: c.Request().ProtoMinor,
					URL:        c.Request().URL.String(),
					URLParams:  urlParamsEcho(c),
					Header:     c.Request().Header,
					Body:       string(reqBody),
				},
				HttpResp: resp,
				Deps:     deps.Deps,
			})

			//fmt.Println("This is the request", c.Request().Proto, u.String(), c.Request().Header, "body - " + string(reqBody), c.Request().Cookies())
			//fmt.Println("This is the response", resBody.String(), c.Response().Header())

			return
		}
	}

}

func captureResp(c echo.Context, next echo.HandlerFunc) keploy.HttpResp {
	resBody := new(bytes.Buffer)
	mw := io.MultiWriter(c.Response().Writer, resBody)
	writer := &bodyDumpResponseWriter{Writer: mw, ResponseWriter: c.Response().Writer}
	c.Response().Writer = writer

	if err := next(c); err != nil {
		c.Error(err)
	}
	return keploy.HttpResp{
		//Status

		StatusCode: c.Response().Status,
		Header:     c.Response().Header(),
		Body:       resBody.String(),
	}
}


func urlParamsEcho (c echo.Context) map[string]string{
	result := pathParamsEcho(c)
	qp := c.QueryParams()
	for i,j := range qp{
		var s string
		if _,ok:=result[i]; ok{
			 s = result[i]
		} 
		for _,e := range j{
			if s!=""{
				s += ", "+e
			} else {
				s = e
			}
		}
		result[i] = s
	}
	return result
}




func pathParamsEcho(c echo.Context) map[string]string{
	var result map[string]string = make(map[string]string)
	paramNames := c.ParamNames()
	paramValues:= c.ParamValues()
	for i:= 0;i<len(paramNames);i++{
		fmt.Printf("paramName : %v, paramValue : %v\n", paramNames[i], paramValues[i])
		result[paramNames[i]] = paramValues[i]
	}
	return result
}

func urlPathEcho(url string, params map[string]string) string{
	res := url
	for i,j := range params{
		res = strings.Replace(url, j, ":"+i, -1)
	}
	return res
}

type bodyDumpResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *bodyDumpResponseWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

func (w *bodyDumpResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func (w *bodyDumpResponseWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *bodyDumpResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func NewMiddlewareContextValue(fn echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		return fn(contextValue{ctx})
	}
}

// from here https://stackoverflow.com/questions/69326129/does-set-method-of-echo-context-saves-the-value-to-the-underlying-context-cont

type contextValue struct {
	echo.Context
}

// Get retrieves data from the context.
func (ctx contextValue) Get(key string) interface{} {
	// get old context value
	val := ctx.Context.Get(key)
	if val != nil {
		return val
	}
	return ctx.Request().Context().Value(keploy.KctxType(key))
}

// Set saves data in the context.
func (ctx contextValue) Set(key string, val interface{}) {

	ctx.SetRequest(ctx.Request().WithContext(context.WithValue(ctx.Request().Context(), keploy.KctxType(key), val)))
}
