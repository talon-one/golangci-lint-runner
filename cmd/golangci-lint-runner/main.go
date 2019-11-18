package main

import (
	"fmt"
	"log"

	"context"

	"os"

	golangci_lint_runner "github.com/talon-one/golangci-lint-runner"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	cloneFlag               = kingpin.Flag("clone", "clone the repository").Bool()
	dirFlag                 = kingpin.Flag("dir", "repository directory (can be omitted if you specify clone=true)").String()
	githubPullRequestIdFlag = kingpin.Flag("github-pull-request-id", "pull request id").PlaceHolder("1234").Required().Int()
	githubRepositoryFlag    = kingpin.Flag("github-repository", "github repository in form of owner/repo").PlaceHolder("owner/repo").Required().String()
	githubTokenFlag         = kingpin.Flag("github-token", "github token, if unspecified takes GITHUB_TOKEN environment variable").Envar("GITHUB_TOKEN").Required().String()
	lintersFlag             = kingpin.Flag("linter", "linter to enable").Required().Strings()
	debugFlag               = kingpin.Flag("debug", "enable debug log").Hidden().Bool()
)

var version string
var commit string
var date string

func main() {
	kingpin.Version(fmt.Sprintf("%s %s %s", version, commit, date))
	kingpin.Parse()

	if !*cloneFlag && *dirFlag == "" {
		fmt.Println("you must either specify --clone or --dir")
		os.Exit(1)
	}

	r := golangci_lint_runner.Runner{
		GithubToken:         *githubTokenFlag,
		GithubPullRequestID: *githubPullRequestIdFlag,
		GithubRepository:    *githubRepositoryFlag,
		Clone:               *cloneFlag,
		GitDirectory:        *dirFlag,
		Logger:              nil,
		Context:             context.Background(),
		Linters:             *lintersFlag,
		IncludeLinterName:   true,
	}

	// enable debug
	if debugFlag != nil && *debugFlag {
		r.Logger = debugLogger{}
	}

	if err := r.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

type debugLogger struct{}

func (debugLogger) Printf(format string, a ...interface{}) {
	log.Printf(format, a...)
}
