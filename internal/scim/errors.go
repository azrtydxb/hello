package scim

import (
	"net/http"
	"strconv"
)

// scimType values (RFC 7644 section 3.12).
const (
	typeInvalidFilter = "invalidFilter"
	typeTooMany       = "tooMany"
	typeUniqueness    = "uniqueness"
	typeMutability    = "mutability"
	typeInvalidSyntax = "invalidSyntax"
	typeInvalidPath   = "invalidPath"
	typeNoTarget      = "noTarget"
	typeInvalidValue  = "invalidValue"
	typeInvalidVers   = "invalidVers"
)

// Error is a SCIM error answer. Detail is safe to show: it never carries a
// token, a password or a stored value of another user.
type Error struct {
	Status  int
	Type    string
	Detail  string
	headers map[string]string
}

func (e *Error) Error() string { return strconv.Itoa(e.Status) + " " + e.Type + ": " + e.Detail }

func (e *Error) body() map[string]any {
	m := map[string]any{"schemas": []string{schemaError}, "status": strconv.Itoa(e.Status), "detail": e.Detail}
	if e.Type != "" {
		m["scimType"] = e.Type
	}
	return m
}

func errInvalidValue(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Type: typeInvalidValue, Detail: detail}
}

func errInvalidSyntax(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Type: typeInvalidSyntax, Detail: detail}
}

func errInvalidFilter(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Type: typeInvalidFilter, Detail: detail}
}

func errInvalidPath(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Type: typeInvalidPath, Detail: detail}
}

func errNoTarget(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Type: typeNoTarget, Detail: detail}
}

func errMutability(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Type: typeMutability, Detail: detail}
}

func errUniqueness(detail string) *Error {
	return &Error{Status: http.StatusConflict, Type: typeUniqueness, Detail: detail}
}

func errNotFound(detail string) *Error {
	return &Error{Status: http.StatusNotFound, Detail: detail}
}

func errStatus(status int, detail string) *Error { return &Error{Status: status, Detail: detail} }
