package golangci_lint_runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/golangci/golangci-lint/pkg/printers"
)

type Result struct {
	Issues           []Issue
	MaxIssuesPerFile int // Needed for gofmt and goimports where it is 1
	ResultJSON       interface{}
}

type Issue struct {
	FromLinter string
	Text       string
	File       string
	LineNumber int
	HunkPos    int
}

func (r *Runner) runLinter(patchFile string) (*Result, error) {
	args := []string{
		"run",
		"--no-config",
		"--out-format=json",
		"--issues-exit-code=0",
		"--disable-all",
		"--new=false",
		fmt.Sprintf("--timeout=%s", r.Timeout),
		fmt.Sprintf("--new-from-patch=%s", patchFile),
	}

	for _, linter := range r.Linters {
		args = append(args, fmt.Sprintf("--enable=%s", linter))
	}

	cmd := exec.Command("golangci-lint", args...)
	r.Logger.Printf("running linter %v in %s\n", cmd.Args, r.GitDirectory)
	cmd.Dir = r.GitDirectory
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("golangci-lint got error: %w", err)
	}

	if err != nil {
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

		return nil, fmt.Errorf("can't run golangci-lint: %w, %s", err, out)
	}

	var res printers.JSONResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("can't run golangci-lint: invalid output json: %s, %w", string(out), err)
	}

	if res.Report != nil && len(res.Report.Warnings) != 0 {
		log.Println("Got golangci-lint warnings: %#v", res.Report.Warnings)
	}

	var retIssues []Issue
	for _, i := range res.Issues {
		retIssues = append(retIssues, Issue{
			File:       i.FilePath(),
			LineNumber: i.Line(),
			Text:       i.Text,
			FromLinter: i.FromLinter,
			HunkPos:    i.HunkPos,
		})
	}
	return &Result{
		Issues:     retIssues,
		ResultJSON: json.RawMessage(out),
	}, nil
}
