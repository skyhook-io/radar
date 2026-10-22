package cnpg

import (
	"errors"
)

var ErrCNPGDisconnected = errors.New("not connected to cluster")

type ReadFailure struct {
	Status  int
	Message string
}

func (e *ReadFailure) Error() string { return e.Message }
