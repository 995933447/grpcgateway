package grpcgateway

import "errors"

var (
	ErrServiceNotFound      = errors.New("service not found")
	ErrNotSupportHttpAccess = errors.New("not support http access")
	ErrNotSupportMethod     = errors.New("not support method")
	ErrNoAuth               = errors.New("no auth")
)
