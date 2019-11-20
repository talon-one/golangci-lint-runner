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

	"path/filepath"

	"github.com/davecgh/go-spew/spew"
	"github.com/google/go-github/github"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	gitHttp "gopkg.in/src-d/go-git.v4/plumbing/transport/http"
)

type BranchMeta struct {
	OwnerName string
	RepoName  string
	FullName  string
	CloneURL  string
	SHA       string
	Ref       string
}

type MetaData struct {
	Base BranchMeta

	Head              BranchMeta
	PullRequestNumber int
}

type Runner struct {
	PullRequest       *github.PullRequest
	CloneToken        string
	GithubClient      *github.Client
	Logger            Logger
	Context           context.Context
	IncludeLinterName bool
	Timeout           time.Duration
	Linters           []string
	meta              MetaData
}

type Logger interface {
	Printf(string, ...interface{})
}

func (r *Runner) Run() error {
	if err := r.getMeta(); err != nil {
		return err
	}
	// prepare workdirectory
	r.Logger.Printf("preparing work directory")
	tmp, err := ioutil.TempDir("", "golangci-lint-runner-github-app")
	if err != nil {
		return fmt.Errorf("unable to create work directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	r.Logger.Printf("work directory is %s\n", tmp)

	if err := os.Setenv("GOPATH", tmp); err != nil {
		return fmt.Errorf("unable to set GOPATH to %s: %w", tmp, err)
	}

	repoDir := filepath.Join(tmp, "src", "github.com", r.meta.Head.FullName)
	if err := os.MkdirAll(repoDir, 0766); err != nil {
		return fmt.Errorf("unable to create repo %s directory: %w", repoDir, err)
	}

	r.Logger.Printf("dir = %s\n", tmp)
	r.Logger.Printf("clone token = %s\n", r.CloneToken)

	if err := r.clone(repoDir); err != nil {
		return err
	}

	patchFile := filepath.Join(tmp, "patch")
	if err := r.downloadPatch(patchFile); err != nil {
		return err
	}

	result, err := r.runLinter(patchFile, repoDir)
	if err != nil {
		return err
	}
	r.Logger.Printf("golangci-lint reported %d issues\n", len(result.Issues))

	reviewRequest := github.PullRequestReviewRequest{
		CommitID: github.String(r.meta.Head.SHA),
		Body:     github.String(fmt.Sprintf("golangci-lint found %d issues", len(result.Issues))),
	}
	if len(result.Issues) <= 0 {
		reviewRequest.Event = github.String("APPROVE")
	} else {
		reviewRequest.Event = github.String("REQUEST_CHANGES")
	}

	for _, issue := range result.Issues {
		if r.IncludeLinterName {
			issue.Text += fmt.Sprintf(" (from %s)", issue.FromLinter)
		}

		comment := github.DraftReviewComment{
			Path:     &issue.File,
			Position: &issue.LineNumber,
			Body:     &issue.Text,
		}

		addToList := true
		for _, c := range reviewRequest.Comments {
			if *c.Path == *comment.Path && *c.Position == *comment.Position && *c.Body == *comment.Body {
				addToList = false
				break
			}
		}
		if addToList {
			reviewRequest.Comments = append(reviewRequest.Comments, &comment)
		}
	}

	r.Logger.Printf("creating review\n")
	r.Logger.Printf(spew.Sdump(reviewRequest))
	_, response, err := r.GithubClient.PullRequests.CreateReview(r.Context, r.meta.Base.OwnerName, r.meta.Base.RepoName, r.meta.PullRequestNumber, &reviewRequest)
	if err != nil {
		return fmt.Errorf("unable to create review: %w\n", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unable to create review: expected 200 got %d", response.StatusCode)
	}
	return nil
}

func (r *Runner) downloadPatch(patchFile string) error {
	r.Logger.Printf("downloading patch file\n")

	s, resp, err := r.GithubClient.PullRequests.GetRaw(context.Background(), r.meta.Base.OwnerName, r.meta.Base.RepoName, r.meta.PullRequestNumber, github.RawOptions{github.Patch})
	if err != nil {
		return fmt.Errorf("unable to download patch file: %w\n", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unable to download patch file: expected 200 but got %d\n", resp.StatusCode)
	}

	return ioutil.WriteFile(patchFile, []byte(s), 0766)
}

func (*Runner) parseOwnerAndRepo(s string) (owner, repo string, err error) {
	p := strings.SplitN(s, "/", 2)
	if len(p) == 2 {
		return p[0], p[1], nil
	}
	return "", "", errors.New("unable to parse repository")
}

func (r *Runner) clone(repoDir string) error {
	branchName := fmt.Sprintf("refs/heads/%s", r.meta.Head.Ref)
	r.Logger.Printf("cloning %s (%s) to %s\n", r.meta.Head.CloneURL, branchName, repoDir)
	_, err := git.PlainCloneContext(r.Context, repoDir, false, &git.CloneOptions{
		URL: r.meta.Head.CloneURL,
		Auth: &gitHttp.BasicAuth{
			// can be anything expect empty
			Username: "x-access-token",
			Password: r.CloneToken,
		},
		ReferenceName:     plumbing.ReferenceName(branchName),
		SingleBranch:      true,
		NoCheckout:        false,
		Depth:             1,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		Tags:              git.NoTags,
	})
	if err != nil {
		return fmt.Errorf("unable to clone git repository %s to %s: %w", r.meta.Head.CloneURL, repoDir, err)
	}
	return nil
}

func (r *Runner) getMeta() error {
	r.Logger.Printf("get meta\n")

	r.meta.PullRequestNumber = r.PullRequest.GetNumber()
	if r.meta.PullRequestNumber == 0 {
		return errors.New("unable to get number from pull request")
	}

	var err error
	base := r.PullRequest.GetBase()
	if base == nil {
		return errors.New("unable to get base")
	}
	r.meta.Base, err = r.getBranchMeta(base)
	if err != nil {
		return fmt.Errorf("unable to get branch meta for base: %w", err)
	}

	head := r.PullRequest.GetHead()
	if head == nil {
		return errors.New("unable to get head")
	}
	r.meta.Head, err = r.getBranchMeta(head)
	if err != nil {
		return fmt.Errorf("unable to get branch meta for head: %w", err)
	}

	return nil
}

func (Runner) getBranchMeta(branch *github.PullRequestBranch) (BranchMeta, error) {
	sha := branch.GetSHA()
	if sha == "" {
		return BranchMeta{}, errors.New("unable to get sha")
	}

	ref := branch.GetRef()
	if ref == "" {
		return BranchMeta{}, errors.New("unable to get ref")
	}

	repo := branch.GetRepo()
	if repo == nil {
		return BranchMeta{}, errors.New("unable to get repo")
	}

	name := repo.GetName()
	if name == "" {
		return BranchMeta{}, errors.New("unable to get repo name")
	}

	fullName := repo.GetFullName()
	if fullName == "" {
		return BranchMeta{}, errors.New("unable to get repo fullname")
	}

	cloneURL := repo.GetCloneURL()
	if cloneURL == "" {
		return BranchMeta{}, errors.New("unable to get repo clone url")
	}

	owner := repo.GetOwner()
	if owner == nil {
		return BranchMeta{}, errors.New("unable to get repo owner")
	}

	login := owner.GetLogin()
	if login == "" {
		return BranchMeta{}, errors.New("unable to get owner login name")
	}

	return BranchMeta{
		OwnerName: login,
		RepoName:  name,
		FullName:  fullName,
		CloneURL:  cloneURL,
		Ref:       ref,
		SHA:       sha,
	}, nil
}
