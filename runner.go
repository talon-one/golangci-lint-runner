package golangci_lint_runner

import (
	"fmt"
	"strings"

	"context"
	"errors"
	"net/http"
	"os"

	"io/ioutil"

	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	gitHttp "gopkg.in/src-d/go-git.v4/plumbing/transport/http"
)

type Runner struct {
	GithubToken               string
	GithubPullRequestID       int
	GithubRepository          string
	githubOwner               string
	githubRepo                string
	githubClient              *github.Client
	Clone                     bool
	commitID                  string
	GitDirectory              string
	cleanGitDirectoryAfterRun bool
	Logger                    Logger
	Context                   context.Context
	Linters                   []string
	IncludeLinterName         bool
	Timeout                   time.Duration
}

type Logger interface {
	Printf(string, ...interface{})
}

func (r *Runner) Run() error {
	var err error
	if err = r.initParams(); err != nil {
		return err
	}

	r.githubOwner, r.githubRepo, err = r.parseOwnerAndRepo(r.GithubRepository)
	if err != nil {
		return fmt.Errorf("unable to parse GithubRepository (%s): %w", r.GithubRepository, err)
	}

	r.Logger.Printf("token = %s\n", r.GithubToken)
	r.Logger.Printf("pr = %d\n", r.GithubPullRequestID)
	r.Logger.Printf("owner = %s\n", r.githubOwner)
	r.Logger.Printf("repo = %s\n", r.githubRepo)
	r.Logger.Printf("dir = %s\n", r.GitDirectory)

	r.githubClient = github.NewClient(oauth2.NewClient(r.Context, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: r.GithubToken},
	)))

	if r.Clone {
		if err := r.clone(); err != nil {
			return err
		}
	} else {
		if err := r.getCommitID(); err != nil {
			return err
		}
	}

	r.Logger.Printf("commitID = %s\n", r.commitID)

	patchFile, err := r.downloadPatch()
	if err != nil {
		return err
	}

	result, err := r.runLinter(patchFile)
	if err != nil {
		return err
	}
	r.Logger.Printf("golangci-lint reported %d issues\n", len(result.Issues))

	r.Logger.Printf("removing patch file\n")
	if err := os.Remove(patchFile); err != nil {
		return fmt.Errorf("unable to remove patch file: %w", err)
	}

	if err := r.clearGitDirectoryAfterRun(); err != nil {
		return err
	}

	comments := make([]*github.DraftReviewComment, len(result.Issues))

	if len(result.Issues) == 0 {
		// no issues found
		reviewRequest := &github.PullRequestReviewRequest{
			CommitID: &r.commitID,
			Body:     github.String("golangci-lint found no issues"),
			Event:    github.String("APPROVE"),
			Comments: comments,
		}
		r.Logger.Printf("creating review\n")
		r.Logger.Printf(spew.Sdump(reviewRequest))
		_, response, err := r.githubClient.PullRequests.CreateReview(r.Context, r.githubOwner, r.githubRepo, r.GithubPullRequestID, reviewRequest)
		if err != nil {
			return fmt.Errorf("unable to create review: %w\n", err)
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("unable to create review: expected 200 got %d", response.StatusCode)
		}
		return nil
	}

	// 1 or more issues found
	for i, issue := range result.Issues {
		if r.IncludeLinterName {
			issue.Text += fmt.Sprintf(" (from %s)", issue.FromLinter)
		}
		comments[i] = &github.DraftReviewComment{
			Path:     &issue.File,
			Position: &issue.LineNumber,
			Body:     &issue.Text,
		}
	}

	reviewRequest := &github.PullRequestReviewRequest{
		CommitID: &r.commitID,
		Body:     github.String(fmt.Sprintf("golangci-lint found %d issues", len(result.Issues))),
		Event:    github.String("REQUEST_CHANGES"),
		Comments: comments,
	}
	r.Logger.Printf("creating review\n")
	r.Logger.Printf(spew.Sdump(reviewRequest))
	_, response, err := r.githubClient.PullRequests.CreateReview(r.Context, r.githubOwner, r.githubRepo, r.GithubPullRequestID, reviewRequest)
	if err != nil {
		return fmt.Errorf("unable to create review: %w\n", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unable to create review: expected 200 got %d", response.StatusCode)
	}
	return nil
}

func (r *Runner) initParams() error {
	if r.GithubToken == "" {
		return errors.New("GithubToken must be set")
	}
	if r.GithubPullRequestID == 0 {
		return errors.New("GithubPullRequestID must be set")
	}
	if r.GithubRepository == "" {
		return errors.New("GithubRepository must be set")
	}
	if !r.Clone {
		if r.GitDirectory == "" {
			return errors.New("GitDirectory must be set")
		}
	} else {
		if r.GitDirectory == "" {
			var err error
			r.GitDirectory, err = ioutil.TempDir("", "golangci-lint-runner")
			if err != nil {
				return fmt.Errorf("unable to create temp directory: %w", err)
			}
		}
		r.cleanGitDirectoryAfterRun = true
	}

	if len(r.Linters) <= 0 {
		return errors.New("Linter is empty, nothing todo")
	}
	if r.Logger == nil {
		r.Logger = dummyLogger{}
	}
	if r.Context == nil {
		r.Context = context.Background()
	}

	if r.Timeout == 0 {
		r.Timeout = time.Minute * 10
	}
	return nil
}

func (r *Runner) downloadPatch() (string, error) {
	r.Logger.Printf("downloading patch file\n")
	s, resp, err := r.githubClient.PullRequests.GetRaw(context.Background(), r.githubOwner, r.githubRepo, r.GithubPullRequestID, github.RawOptions{github.Patch})
	if err != nil {
		return "", fmt.Errorf("unable to download patch file: %w\n", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unable to download patch file: expected 200 but got %d\n", resp.StatusCode)
	}

	tmpfile, err := ioutil.TempFile("", "patch")
	if err != nil {
		return "", fmt.Errorf("unable to create patch file: %w\n", err)
	}

	if _, err := tmpfile.WriteString(s); err != nil {
		return "", fmt.Errorf("unable to write patch file: %w\n", err)
	}

	if err := tmpfile.Close(); err != nil {
		return "", fmt.Errorf("unable to close patch file: %w\n", err)
	}
	r.Logger.Printf("got patchfile %s\n", tmpfile.Name())
	return tmpfile.Name(), nil
}

func (*Runner) parseOwnerAndRepo(s string) (owner, repo string, err error) {
	p := strings.SplitN(s, "/", 2)
	if len(p) == 2 {
		return p[0], p[1], nil
	}
	return "", "", errors.New("unable to parse repository")
}

func (r *Runner) clone() error {
	pr, response, err := r.githubClient.PullRequests.Get(r.Context, r.githubOwner, r.githubRepo, r.GithubPullRequestID)
	if err != nil {
		return fmt.Errorf("unable to get pull request: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unable to get pull request: expected 200 got %d", response.StatusCode)
	}
	head := pr.GetHead()
	if head == nil {
		return errors.New("unable to get head")
	}

	r.commitID = head.GetSHA()
	if r.commitID == "" {
		return errors.New("unable to get head sha")
	}

	ref := head.GetRef()
	if ref == "" {
		return errors.New("unable to get head reference")
	}

	repo, response, err := r.githubClient.Repositories.Get(r.Context, r.githubOwner, r.githubRepo)
	if err != nil {
		return fmt.Errorf("unable to get repository: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unable to get repository: expected 200 got %d", response.StatusCode)
	}

	// clear the destination
	r.Logger.Printf("clearing %s\n", r.GitDirectory)
	if err := os.RemoveAll(r.GitDirectory); err != nil {
		return fmt.Errorf("unable to clean git directory: %w", err)
	}

	branchName := fmt.Sprintf("refs/heads/%s", ref)
	r.Logger.Printf("cloning %s (%s) to %s\n", repo.GetCloneURL(), branchName, r.GitDirectory)
	_, err = git.PlainCloneContext(r.Context, r.GitDirectory, false, &git.CloneOptions{
		URL: repo.GetCloneURL(),
		Auth: &gitHttp.BasicAuth{
			// can be anything expect empty
			Username: "golangci-lint-runner",
			Password: r.GithubToken,
		},
		ReferenceName:     plumbing.ReferenceName(branchName),
		SingleBranch:      true,
		NoCheckout:        false,
		Depth:             1,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		Tags:              git.NoTags,
	})
	if err != nil {
		return fmt.Errorf("unable to clone git repository %s: %w", r.GitDirectory, err)
	}
	return nil
}

func (r *Runner) getCommitID() error {
	repository, err := git.PlainOpen(r.GitDirectory)
	if err != nil {
		return fmt.Errorf("unable to open git repository %s: %w", r.GitDirectory, err)
	}

	head, err := repository.Head()
	if err != nil {
		return fmt.Errorf("unable to get head from git repository (%s): %w", r.GitDirectory, err)
	}

	r.commitID = head.Hash().String()
	return nil
}

func (r *Runner) clearGitDirectoryAfterRun() error {
	if !r.cleanGitDirectoryAfterRun {
		return nil
	}

	r.Logger.Printf("cleaning git directory\n")
	if err := os.RemoveAll(r.GitDirectory); err != nil {
		return fmt.Errorf("unable to clean git directory: %w", err)
	}
	return nil
}
