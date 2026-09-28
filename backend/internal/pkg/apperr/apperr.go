// Package apperr 定义全系统统一的领域错误。
//
// 设计目标：
//  1. service 层只返回 *apperr.Error，不感知 HTTP；
//  2. handler 层统一翻译成 HTTP 状态码 + 统一响应体；
//  3. 错误码形如 HTTP状态码*100+序号（如 40401），前端既能按 HTTP 语义处理，
//     也能按业务码精确判断。
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code 业务错误码。
type Code int

// 约定的错误码。新增错误码必须同时在 httpStatus 中登记，否则退化为 500。
const (
	CodeOK Code = 0

	// 400 参数/请求错误
	CodeInvalidParam  Code = 40001
	CodeBindingFailed Code = 40002
	CodeWeakPassword  Code = 40003

	// 401 未认证
	CodeUnauthorized    Code = 40101
	CodeTokenExpired    Code = 40102
	CodeBadCredentials  Code = 40103
	CodeAccountInactive Code = 40104

	// 403 已认证但无权限
	CodeForbidden Code = 40301

	// 404 资源不存在（含"不可见"，用于防止枚举探测）
	CodeNotFound Code = 40401

	// 409 状态冲突
	CodeConflict         Code = 40900
	CodeRevisionConflict Code = 40901
	CodeFolderNotEmpty   Code = 40902
	CodeDuplicateName    Code = 40903
	CodeUsernameTaken    Code = 40904
	CodeCannotSelfLock   Code = 40905

	// 422 语义错误
	CodeUnprocessable  Code = 42201
	CodeCrossSubject   Code = 42202
	CodeMissingRef     Code = 42203
	CodeInvalidAnswer  Code = 42204
	CodeAnswerMismatch Code = 42205

	// 429 限流
	CodeTooManyRequests Code = 42901

	// 500 服务端
	CodeInternal     Code = 50001
	CodeExportFailed Code = 50003

	// 503 依赖未就绪 / 未初始化
	//
	// AI 未配置属于"服务暂时不可用"而不是"服务器内部错误"：
	// 它是一个可预期的配置状态，返回 500 会让人以为是程序 bug，
	// 也会让前端的错误处理走到"请联系管理员"这种无用的分支上。
	CodeSetupRequired Code = 50301
	CodeAIUnavailable Code = 50302
)

// httpStatus 错误码 → HTTP 状态码。取错误码的前三位。
func httpStatus(code Code) int {
	if code == CodeOK {
		return http.StatusOK
	}
	s := int(code) / 100
	if s < 100 || s > 599 {
		return http.StatusInternalServerError
	}
	return s
}

// Error 领域错误。实现 error 接口，可携带底层原因但不会向外暴露。
type Error struct {
	Code    Code
	Message string // 面向用户的中文提示，可直接展示
	Detail  string // 面向开发者的补充信息，可选
	cause   error  // 底层错误，仅用于日志
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("[%d] %s: %s", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// HTTPStatus 该错误应返回的 HTTP 状态码。
func (e *Error) HTTPStatus() int { return httpStatus(e.Code) }

// WithDetail 附加开发者细节，返回新实例（不修改原值，便于复用哨兵错误）。
func (e *Error) WithDetail(format string, args ...any) *Error {
	return &Error{Code: e.Code, Message: e.Message, Detail: fmt.Sprintf(format, args...), cause: e.cause}
}

// WithCause 附加底层原因（只进日志）。调用方需自行决定细节是否对外可见。
func (e *Error) WithCause(err error) *Error {
	return &Error{Code: e.Code, Message: e.Message, Detail: e.Detail, cause: err}
}

// WithMessage 覆盖用户可见提示。
func (e *Error) WithMessage(msg string) *Error {
	return &Error{Code: e.Code, Message: msg, Detail: e.Detail, cause: e.cause}
}

// New 构造一个领域错误。
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// 常用哨兵错误。使用处可通过 WithDetail/WithMessage 派生，避免重复字面量。
var (
	ErrUnauthorized   = New(CodeUnauthorized, "请先登录")
	ErrTokenExpired   = New(CodeTokenExpired, "登录已过期，请重新登录")
	ErrBadCredentials = New(CodeBadCredentials, "用户名或密码错误")
	ErrForbidden      = New(CodeForbidden, "没有执行该操作的权限")
	ErrNotFound       = New(CodeNotFound, "资源不存在")
	ErrConflict       = New(CodeRevisionConflict, "数据已被他人修改，请刷新后重试")
	ErrUnprocessable  = New(CodeUnprocessable, "请求无法处理")
	ErrInternal       = New(CodeInternal, "服务器内部错误")
	ErrSetupRequired  = New(CodeSetupRequired, "系统尚未完成初始化")
)

// From 把任意 error 规整为 *Error。非领域错误一律视为 500 并保留原始原因。
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return ErrInternal.WithCause(err)
}

// Is 判断 err 链上是否存在指定错误码。
func Is(err error, code Code) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == code
	}
	return false
}
