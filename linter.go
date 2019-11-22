package golangci_lint_runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"os"

	"github.com/golangci/golangci-lint/pkg/printers"
)

func (runner *Runner) runLinter(cacheDir, patchFile, workDir, repoDir string) (*printers.JSONResult, error) {
	args := []string{

		"run",
		"--no-config",
		"--out-format=json",
		"--issues-exit-code=0",
		"--disable-all",
		"--new=false",
		fmt.Sprintf("--timeout=%s", runner.Options.Timeout.String()),
		fmt.Sprintf("--new-from-patch=%s", patchFile),
	}

	for _, linter := range runner.linterOptions.Linters {
		args = append(args, fmt.Sprintf("--enable=%s", linter))
	}

	cmd := exec.Command("golangci-lint", args...)
	runner.Options.Logger.Debug("running linter %v in %s", cmd.Args, repoDir)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("GOPATH=%s", workDir), fmt.Sprintf("GOLANGCI_LINT_CACHE=%s", cacheDir))

	out, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			if len(e.Stderr) > 0 {
				err = errors.New(e.ProcessState.String() + "\nStderr: " + string(e.Stderr))
			}
		}

		return nil, fmt.Errorf("golangci-lint got error: %w", err)
	}

	var res printers.JSONResult
	if err := json.Unmarshal(out, &res); err == nil && res.Report.Error != "" {
		return nil, fmt.Errorf("can't run golangci-lint: %w", res.Report.Error)
	}

	const badLoadStr = "failed to load program with go/packages"
	if strings.Contains(err.Error(), badLoadStr) {
		ind := strings.Index(err.Error(), badLoadStr)
		if ind < len(err.Error())-1 {
			return nil, errors.New(err.Error()[ind:])
		}
	}

	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("can't run golangci-lint: invalid output json: %s, %w", string(out), err)
	}

	return &res, nil
}
