package central

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidResponse     = errors.New("invalid central response")
	ErrAuthorizationDenied = errors.New("central charge authorization denied")
	ErrResultConflict      = errors.New("central rejected device result")
)

type BusinessError struct{ Code int }

func (e BusinessError) Error() string {
	return fmt.Sprintf("central rejected request: code %d", e.Code)
}
