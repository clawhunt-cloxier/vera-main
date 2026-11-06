// Package errs defines coded application errors mapped to the API error envelope.
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
	cause     error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

func New(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Message: msg} }

func (e *Error) With(k string, v any) *Error {
	c := *e
	c.Details = map[string]any{}
	for kk, vv := range e.Details {
		c.Details[kk] = vv
	}
	c.Details[k] = v
	return &c
}

func (e *Error) Wrap(err error) *Error { c := *e; c.cause = err; return &c }

func BadRequest(code, msg string) *Error   { return New(http.StatusBadRequest, code, msg) }
func NotFound() *Error                     { return New(http.StatusNotFound, "NOT_FOUND", "资源不存在") }
func Conflict(code, msg string) *Error     { return New(http.StatusConflict, code, msg) }
func Unprocessable(code, msg string) *Error { return New(http.StatusUnprocessableEntity, code, msg) }
func Unauthorized() *Error                 { return New(http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录") }
func Forbidden(code, msg string) *Error    { return New(http.StatusForbidden, code, msg) }
func TooMany(code, msg string) *Error {
	e := New(http.StatusTooManyRequests, code, msg)
	e.Retryable = true
	return e
}
func Unavailable(code, msg string) *Error {
	e := New(http.StatusServiceUnavailable, code, msg)
	e.Retryable = true
	return e
}

// As extracts an *Error or returns an internal error that hides the cause from users.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return New(http.StatusInternalServerError, "INTERNAL", "服务内部错误").Wrap(err)
}
