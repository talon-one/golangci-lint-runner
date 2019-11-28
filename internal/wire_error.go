package internal

type WireError struct {
	StatusCode   int
	PublicError  error
	PrivateError error
}

func (e WireError) Error() string { return e.PublicError.Error() }
