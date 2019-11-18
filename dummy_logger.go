package golangci_lint_runner

type dummyLogger struct{}

func (dummyLogger) Printf(string, ...interface{}) {
}
