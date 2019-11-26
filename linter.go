package golangci_lint_runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"os"

	"bufio"
	"io"
	"strconv"
	"strings"

	"github.com/golangci/golangci-lint/pkg/printers"
	"github.com/golangci/golangci-lint/pkg/result"
)

func (runner *Runner) runLinter(cacheDir, workDir, repoDir string) (*printers.JSONResult, error) {
	args := []string{

		"run",
		"--no-config",
		"--out-format=json",
		"--issues-exit-code=0",
		"--disable-all",
		// "--new=false",
		"--exclude-use-default=false",
		fmt.Sprintf("--timeout=%s", runner.Options.Timeout.String()),
		// fmt.Sprintf("--new-from-patch=%s", patchFile),
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
	err = json.Unmarshal(out, &res)
	if err != nil {
		return nil, fmt.Errorf("can't run golangci-lint: invalid output json: %s, %w", string(out), err)
	}

	if res.Report != nil && res.Report.Error != "" {
		return nil, fmt.Errorf("can't run golangci-lint: %w", res.Report.Error)
	}

	return &res, nil
}

func filterIssues(patchFile string, issues []result.Issue) ([]result.Issue, error) {
	f, err := os.Open(patchFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, err := linesChanged(f)
	if err != nil {
		return nil, err
	}
	var filteredIssues []result.Issue
	for _, i := range issues {
		positions, ok := m[i.FilePath()]
		if !ok {
			continue
		}
		for _, pos := range positions {
			if pos.lineNo == i.Line() {
				i.HunkPos = pos.hunkPos
				filteredIssues = append(filteredIssues, i)
				break
			}
		}
	}
	return filteredIssues, nil
}

type pos struct {
	lineNo  int // line number
	hunkPos int // position relative to first @@ in file
}

// Readln returns a single line (without the ending \n)
// from the input buffered reader.
// An error is returned iff there is an error with the
// buffered reader.
func Readln(r *bufio.Reader) (string, error) {
	var (
		isPrefix bool  = true
		err      error = nil
		line, ln []byte
	)
	for isPrefix && err == nil {
		line, isPrefix, err = r.ReadLine()
		ln = append(ln, line...)
	}
	return string(ln), err
}

//
// linesChanges returns a map of file names to line numbers being changed.
// If key is nil, the file has been recently added, else it contains a slice
// of positions that have been added.
func linesChanged(patch io.Reader) (map[string][]pos, error) {
	type state struct {
		file    string
		lineNo  int   // current line number within chunk
		hunkPos int   // current line count since first @@ in file
		changes []pos // position of changes
	}

	var (
		s       state
		changes = make(map[string][]pos)
	)

	reader := bufio.NewReader(patch)
	line, err := Readln(reader)
	for err == nil {
		// c.debugf(line)
		s.lineNo++
		s.hunkPos++
		switch {
		case strings.HasPrefix(line, "+++ ") && len(line) > 4:
			if s.changes != nil {
				// record the last state
				changes[s.file] = s.changes
			}
			// 6 removes "+++ b/"
			s = state{file: line[6:], hunkPos: -1, changes: []pos{}}
		case strings.HasPrefix(line, "@@ "):
			//      @@ -1 +2,4 @@
			// chdr ^^^^^^^^^^^^^
			// ahdr       ^^^^
			// cstart      ^
			chdr := strings.Split(line, " ")
			ahdr := strings.Split(chdr[2], ",")
			// [1:] to remove leading plus
			cstart, err := strconv.ParseUint(ahdr[0][1:], 10, 64)
			if err != nil {
				panic(err)
			}
			s.lineNo = int(cstart) - 1 // -1 as cstart is the next line number
		case strings.HasPrefix(line, "-"):
			s.lineNo--
		case strings.HasPrefix(line, "+"):
			s.changes = append(s.changes, pos{lineNo: s.lineNo, hunkPos: s.hunkPos})
		}
		line, err = Readln(reader)
	}
	if err == io.EOF {
		err = nil
	}
	// record the last state
	changes[s.file] = s.changes
	return changes, err
}
