package golangci_lint_runner

import (
	"fmt"

	"context"
	"errors"
	"net/http"
	"os"

	"io/ioutil"

	"path/filepath"

	"crypto/rsa"
	"strconv"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/dgrijalva/jwt-go"
	"github.com/google/go-github/github"
	"github.com/talon-one/golangci-lint-runner/internal"
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
	InstallationID    int64
}

type Runner struct {
	Context      context.Context
	Installation *github.Installation
	PullRequest  *github.PullRequest

	appClient          *github.Client
	installationClient *github.Client

	meta              MetaData
	installationToken *github.InstallationToken

	Options       *Options
	linterOptions *LinterOptions
}

func NewRunner(context context.Context, installation *github.Installation, pullRequest *github.PullRequest, options *Options) (*Runner, error) {
	runner := Runner{
		Context:      context,
		Installation: installation,
		PullRequest:  pullRequest,
		Options:      options,
	}
	if err := runner.getMeta(); err != nil {
		return nil, err
	}

	var err error
	runner.appClient, err = makeAppClient(runner.Options.AppID, options.PrivateKey)
	if err != nil {
		return nil, internal.WireError{
			PrivateError: fmt.Errorf("unable to create client"),
		}
	}

	options.Logger.Debug("creating installation token")
	// todo: we can store this token for a later use
	runner.installationToken, _, err = runner.appClient.Apps.CreateInstallationToken(context, runner.meta.InstallationID)
	if err != nil {
		return nil, internal.WireError{
			PrivateError: fmt.Errorf("unable to create installation token: %w", err),
		}
	}
	if runner.installationToken.GetToken() == "" {
		return nil, internal.WireError{
			PrivateError: errors.New("unable to get installation token"),
		}
	}

	runner.installationClient, err = makeInstallationClient(runner.installationToken)
	if err != nil {
		return nil, internal.WireError{
			PrivateError: fmt.Errorf("unable to create client"),
		}
	}

	// test permissions
	// _, _, err = runner.installationClient.PullRequests.Get(context, pullRequest.GetHead().GetUser().GetLogin(), pullRequest.GetHead().GetRepo().GetName(), pullRequest.GetNumber())
	// if err != nil {
	// 	return nil, internal.WireError{
	// 		PublicError:  errors.New("permissions failed (installationClient)"),
	// 		PrivateError: fmt.Errorf("permissions failed (installationClient): %w", err),
	// 	}
	// }

	return &runner, nil
}

func (runner *Runner) Run() error {
	// prepare work directory
	runner.Options.Logger.Debug("preparing work directory")
	workDir, err := ioutil.TempDir("", "golangci-lint-runner")
	if err != nil {
		return fmt.Errorf("unable to create work directory: %w", err)
	}
	// remove work directory on end
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			runner.Options.Logger.Error("unable to delete work directory: %w", err)
		}
	}()

	runner.Options.Logger.Debug("work directory is %s", workDir)

	// todo: replace github.com with some response from api
	repoDir := filepath.Join(workDir, "src", "github.com", runner.meta.Head.FullName)
	if err := os.MkdirAll(repoDir, 0744); err != nil {
		return fmt.Errorf("unable to create repo %s directory: %w", repoDir, err)
	}
	runner.Options.Logger.Debug("repo directory is %s", repoDir)

	if err := runner.clone(repoDir); err != nil {
		return err
	}

	//todo: read linte roptions from repository, for now just copy the defaults
	runner.linterOptions = &runner.Options.DefaultLinterOptions

	patchFile := filepath.Join(workDir, "patch")
	if err := runner.downloadPatch(patchFile); err != nil {
		return err
	}

	result, err := runner.runLinter(patchFile, workDir, repoDir)
	if err != nil {
		return err
	}
	runner.Options.Logger.Info("golangci-lint reported %d issues for %s", len(result.Issues), runner.meta.Head.FullName)

	reviewRequest := github.PullRequestReviewRequest{
		CommitID: github.String(runner.meta.Head.SHA),
		Body:     github.String(fmt.Sprintf("golangci-lint found %d issues", len(result.Issues))),
	}
	if len(result.Issues) <= 0 {
		reviewRequest.Event = github.String("APPROVE")
	} else {
		reviewRequest.Event = github.String("REQUEST_CHANGES")
	}

	for _, issue := range result.Issues {
		if runner.linterOptions.IncludeLinterName {
			issue.Text += fmt.Sprintf(" (from %s)", issue.FromLinter)
		}

		comment := github.DraftReviewComment{
			Path:     &issue.File,
			Position: &issue.HunkPos,
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

	runner.Options.Logger.Debug("creating review")
	runner.Options.Logger.Debug(spew.Sdump(reviewRequest))
	_, response, err := runner.installationClient.PullRequests.CreateReview(runner.Context, runner.meta.Base.OwnerName, runner.meta.Base.RepoName, runner.meta.PullRequestNumber, &reviewRequest)
	if err != nil {
		return fmt.Errorf("unable to create review: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unable to create review: expected 200 got %d", response.StatusCode)
	}
	return nil
}

func (runner *Runner) downloadPatch(patchFile string) error {
	runner.Options.Logger.Debug("downloading patch file")
	s, _, err := runner.installationClient.PullRequests.GetRaw(context.Background(), runner.meta.Base.OwnerName, runner.meta.Base.RepoName, runner.meta.PullRequestNumber, github.RawOptions{github.Diff})
	if err != nil {
		return fmt.Errorf("unable to download patch file: %w", err)
	}

	return ioutil.WriteFile(patchFile, []byte(s), 0744)
}

func (runner *Runner) clone(repoDir string) error {
	branchName := fmt.Sprintf("refs/heads/%s", runner.meta.Head.Ref)
	runner.Options.Logger.Debug("cloning %s (%s) to %s", runner.meta.Head.CloneURL, branchName, repoDir)
	_, err := git.PlainCloneContext(runner.Context, repoDir, false, &git.CloneOptions{
		URL: runner.meta.Head.CloneURL,
		Auth: &gitHttp.BasicAuth{
			// can be anything expect empty
			Username: "x-access-token",
			Password: runner.installationToken.GetToken(),
		},
		ReferenceName:     plumbing.ReferenceName(branchName),
		SingleBranch:      true,
		NoCheckout:        false,
		Depth:             1,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		Tags:              git.NoTags,
	})
	if err != nil {
		return fmt.Errorf("unable to clone git repository %s to %s: %w", runner.meta.Head.CloneURL, repoDir, err)
	}
	return nil
}

func (runner *Runner) getMeta() error {
	runner.Options.Logger.Debug("get meta")

	runner.meta.InstallationID = runner.Installation.GetID()
	if runner.meta.InstallationID == 0 {
		return errors.New("unable to get id from installation")
	}

	runner.meta.PullRequestNumber = runner.PullRequest.GetNumber()
	if runner.meta.PullRequestNumber == 0 {
		return errors.New("unable to get number from pull request")
	}

	var err error
	base := runner.PullRequest.GetBase()
	if base == nil {
		return errors.New("unable to get base")
	}
	runner.meta.Base, err = runner.getBranchMeta(base)
	if err != nil {
		return fmt.Errorf("unable to get branch meta for base: %w", err)
	}

	head := runner.PullRequest.GetHead()
	if head == nil {
		return errors.New("unable to get head")
	}
	runner.meta.Head, err = runner.getBranchMeta(head)
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

type appTransport struct {
	underlyingTransport http.RoundTripper
	token               string
}

func (t *appTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Add("Accept", "application/vnd.github.machine-man-preview+json")
	req.Header.Add("Authorization", "Bearer "+t.token)
	return t.underlyingTransport.RoundTrip(req)
}

func makeAppClient(appID int64, privateKey *rsa.PrivateKey) (*github.Client, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.StandardClaims{
		ExpiresAt: time.Now().Local().Add(time.Minute * 5).Unix(),
		IssuedAt:  time.Now().Unix(),
		Issuer:    strconv.FormatInt(appID, 10),
	})

	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		return nil, err
	}
	return github.NewClient(&http.Client{Transport: &appTransport{underlyingTransport: http.DefaultTransport, token: tokenString}}), nil
}

type installationTransport struct {
	underlyingTransport http.RoundTripper
	token               string
}

func (t *installationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Add("Accept", "application/vnd.github.machine-man-preview+json")
	req.Header.Add("Authorization", "token "+t.token)
	return t.underlyingTransport.RoundTrip(req)
}

func makeInstallationClient(installationToken *github.InstallationToken) (*github.Client, error) {
	return github.NewClient(&http.Client{Transport: &installationTransport{underlyingTransport: http.DefaultTransport, token: installationToken.GetToken()}}), nil
}
