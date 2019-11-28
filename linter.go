package golangci_lint_runner

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"os"

	"bufio"
	"io"
	"strconv"
	"strings"

	"path/filepath"

	"errors"

	"github.com/golangci/golangci-lint/pkg/printers"
	"github.com/golangci/golangci-lint/pkg/result"
	jsoniter "github.com/json-iterator/go"
)

func (runner *Runner) runLinter(cacheDir, workDir, repoDir string) (*printers.JSONResult, error) {
	configPath, err := runner.generateConfig(workDir)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("golangci-lint", "run", "--config="+configPath)
	cmd.Dir = repoDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GOPATH=" + workDir,
		"GOCACHE=" + cacheDir,
		"GOROOT=" + os.Getenv("GOROOT"),
		"HOME=" + cacheDir,
	}

	runner.Options.Logger.Debug("running linter %v in %s %v", cmd.Args, repoDir, cmd.Env)

	out, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			var sb strings.Builder
			if len(out) > 0 {
				sb.WriteString("\nStdout:\n")
				sb.Write(out)
				sb.WriteRune('\n')
			}
			if len(e.Stderr) > 0 {
				sb.WriteString("\nStderr:\n")
				sb.Write(e.Stderr)
				sb.WriteRune('\n')
			}
			err = errors.New(e.String() + sb.String())
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

func (runner *Runner) generateConfig(workDir string) (string, error) {
	configPath := filepath.Join(workDir, "golangci-lint.json")
	file, err := os.Create(configPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	runner.Options.LinterConfig.Run.IsVerbose = false
	runner.Options.LinterConfig.Run.Silent = false
	// runner.Options.LinterConfig.Run.CPUProfilePath -- use parent
	// runner.Options.LinterConfig.Run.MemProfilePath -- use parent
	// runner.Options.LinterConfig.Run.TracePath -- use parent
	runner.Options.LinterConfig.Run.Concurrency = 0
	runner.Options.LinterConfig.Run.PrintResourcesUsage = false
	runner.Options.LinterConfig.Run.Config = configPath
	runner.Options.LinterConfig.Run.NoConfig = false
	runner.Options.LinterConfig.Run.Args = nil
	// runner.Options.LinterConfig.Run.BuildTags -- use parent
	runner.Options.LinterConfig.Run.ModulesDownloadMode = "" // use default
	runner.Options.LinterConfig.Run.ExitCodeIfIssuesFound = 0
	// runner.Options.LinterConfig.Run.AnalyzeTests -- use parent

	runner.Options.LinterConfig.Run.Timeout = runner.Options.Timeout

	runner.Options.LinterConfig.Run.PrintVersion = false
	// runner.Options.LinterConfig.Run.SkipFiles -- use parent
	// runner.Options.LinterConfig.Run.SkipDirs -- use parent
	// runner.Options.LinterConfig.Run.UseDefaultSkipDirs -- use parent

	runner.Options.LinterConfig.Output.Format = "json"
	runner.Options.LinterConfig.Output.Color = "never"
	runner.Options.LinterConfig.Output.PrintIssuedLine = true
	runner.Options.LinterConfig.Output.PrintWelcomeMessage = false

	// runner.Options.LinterConfig.LintersSettings -- use parent
	// runner.Options.LinterConfig.Linters -- use parent

	// runner.Options.LinterConfig.Issues.ExcludePatterns -- use parent
	// runner.Options.LinterConfig.Issues.ExcludeRules -- use parent
	// runner.Options.LinterConfig.Issues.UseDefaultExcludes -- use parent

	runner.Options.LinterConfig.Issues.MaxIssuesPerLinter = 0
	runner.Options.LinterConfig.Issues.MaxSameIssues = 0

	runner.Options.LinterConfig.Issues.DiffFromRevision = ""
	runner.Options.LinterConfig.Issues.DiffPatchFilePath = ""
	runner.Options.LinterConfig.Issues.Diff = false

	//runner.Options.LinterConfig.Issues.NeedFix -- use parent

	var json = jsoniter.Config{
		EscapeHTML:             true,
		SortMapKeys:            true,
		ValidateJsonRawMessage: true,
		TagKey:                 "mapstructure",
	}.Froze()

	return configPath, json.NewEncoder(file).Encode(runner.Options.LinterConfig)
}

func hasGoCode(patchFile string) (bool, error) {
	f, err := os.Open(patchFile)
	if err != nil {
		return false, err
	}
	defer f.Close()
	m, err := linesChanged(f)
	if err != nil {
		return false, err
	}

	for k := range m {
		if strings.HasSuffix(k, ".go") {
			return true, nil
		}
	}

	return false, nil
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

// readLine returns a single line (without the ending \n)
// from the input buffered reader.
// An error is returned iff there is an error with the
// buffered reader.
func readLine(r *bufio.Reader) (string, error) {
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

// stolen from revgrep
// horrible code please change
//
// linesChanges returns a map of file names to line numbers being changed.
// If key is nil, the file has been recently added, else it contains a slice
// of positions that have been added.
func linesChanged(patch io.Reader) (map[string][]pos, error) {
	var file string
	var lineNo int    // current line number within chunk
	var hunkPos int   // current line count since first @@ in file
	var changes []pos // position of changes
	// var hasHunk bool

	fileChanges := make(map[string][]pos)

	reader := bufio.NewReader(patch)
	line, err := readLine(reader)
	for err == nil {
		// c.debugf(line)
		lineNo++
		hunkPos++
		switch {
		case strings.HasPrefix(line, "+++ ") && len(line) > 4:
			if changes != nil {
				// record the last state
				fileChanges[file] = changes
			}
			// 6 removes "+++ b/"
			file = line[6:]
			// if !hasHunk {
			hunkPos = -1
			// hasHunk = true
			// }

			changes = []pos{}
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
			lineNo = int(cstart) - 1 // -1 as cstart is the next line number
		case strings.HasPrefix(line, "-"):
			lineNo--
		case strings.HasPrefix(line, "+"):
			changes = append(changes, pos{lineNo: lineNo, hunkPos: hunkPos})
		}
		line, err = readLine(reader)
	}
	if err == io.EOF {
		err = nil
	}
	// record the last state
	fileChanges[file] = changes
	return fileChanges, err
}
