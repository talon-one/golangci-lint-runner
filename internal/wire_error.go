package internal

type WireError struct {
	StatusCode   int
	PublicError  error
	PrivateError error
}

func (WireError) Error() string { return "" }
