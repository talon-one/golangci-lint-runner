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

	"strings"

	"encoding/json"

	"github.com/dgrijalva/jwt-go"
	"github.com/golangci/golangci-lint/pkg/config"
	"github.com/golangci/golangci-lint/pkg/report"
	"github.com/google/go-github/github"
	"github.com/imdario/mergo"
	"github.com/spf13/viper"
	"github.com/talon-one/golangci-lint-runner/internal"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	gitHttp "gopkg.in/src-d/go-git.v4/plumbing/transport/http"
)

type Options struct {
	Client            *github.Client
	CloneToken        string
	Context           context.Context
	PullRequest       *github.PullRequest
	Name              string
	Owner             string
	PullRequestNumber int
	Logger            Logger
	Timeout           time.Duration
	LinterConfig      config.Config
	CacheDir          string
	Approve           bool
	RequestChanges    bool
	DryRun            bool
	// NoChangesText sends the text when no go code changes are present
	NoChangesText   string
	NoIssuesText    string
	NoNewIssuesText string
}

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
	PullRequestURL    string
	InstallationID    int64
}

type Runner struct {
	meta    MetaData
	Options *Options
}

const (
	githubEventApprove        = "APPROVE"
	githubEventRequestChanges = "REQUEST_CHANGES"
	githubEventComment        = "COMMENT"
)

func NewRunner(options Options) (*Runner, error) {
	if options.Client == nil {
		return nil, errors.New("Client must be specified")
	}
	if options.CloneToken == "" {
		return nil, errors.New("CloneToken must be specified")
	}
	if options.Context == nil {
		return nil, errors.New("Context must be specified")
	}
	if options.Logger == nil {
		return nil, errors.New("Logger must be specified")
	}
	if options.Timeout <= 0 {
		options.Timeout = time.Minute * 10
	}
	runner := Runner{
		Options: &options,
	}

	if runner.Options.CacheDir == "" {
		var err error
		runner.Options.CacheDir, err = ioutil.TempDir("", "golangci-lint-runner-cache-")
		if err != nil {
			return nil, internal.WireError{
				PrivateError: fmt.Errorf("unable to create cache dir: %w", err),
			}
		}
	}

	if runner.Options.PullRequest == nil {
		var err error
		runner.Options.Logger.Debug("getting pull request")
		runner.Options.PullRequest, _, err = runner.Options.Client.PullRequests.Get(runner.Options.Context, runner.Options.Owner, runner.Options.Name, runner.Options.PullRequestNumber)
		if err != nil {
			return nil, internal.WireError{
				PublicError:  errors.New("unable to get pull request"),
				PrivateError: fmt.Errorf("unable to get pull request: %w", err),
			}
		}
	}

	if err := runner.getMeta(); err != nil {
		return nil, err
	}

	return &runner, nil
}

func (runner *Runner) Run() error {
	// prepare work directory
	startTime := time.Now()
	runner.Options.Logger.Info("starting with pull request %s", runner.meta.PullRequestURL)
	runner.Options.Logger.Debug("preparing work directory")
	workDir, err := ioutil.TempDir("", "golangci-lint-runner-work-")
	if err != nil {
		return fmt.Errorf("unable to create work directory: %w", err)
	}
	// remove work directory on end
	defer func() {
		uid := os.Getuid()
		gid := os.Getegid()

		err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return nil
			}
			if err = os.Chown(path, uid, gid); err != nil {
				return err
			}
			if err = os.Chmod(path, 0700); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			runner.Options.Logger.Error("unable to change permissions for work directory: %w", err)
		}
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

	if err := runner.readRepoConfig(repoDir); err != nil {
		return err
	}

	patchFile := filepath.Join(workDir, "patch")
	if err := runner.downloadPatch(patchFile); err != nil {
		return err
	}

	reviewRequest := github.PullRequestReviewRequest{
		CommitID: github.String(runner.meta.Head.SHA),
	}

	goCode, err := hasGoCode(patchFile)
	if err != nil {
		return fmt.Errorf("unable to detect go code: %w", err)
	}
	if !goCode {
		runner.Options.Logger.Debug("no go code present")
		reviewRequest.Body = github.String(runner.Options.NoChangesText)
		if runner.Options.Approve {
			reviewRequest.Event = github.String(githubEventApprove)
		} else {
			reviewRequest.Event = github.String(githubEventComment)
		}
		return runner.sendReview(&reviewRequest)
	}

	result, err := runner.runLinter(runner.Options.CacheDir, workDir, repoDir)
	if err != nil {
		return err
	}

	var warnings []report.Warning
	if result.Report != nil {
		warnings = result.Report.Warnings
	}
	runner.Options.Logger.Debug("golangci-lint reported %d (unfiltered) issues and %d warnings for %s", len(result.Issues), len(warnings), runner.meta.Head.FullName)

	runner.Options.Logger.Debug("filtering issues")

	// for _, issue := range result.Issues {
	// 	fmt.Printf("%s:%d: %s (from %s)\n", issue.FilePath(), issue.Line(), issue.Text, issue.FromLinter)
	// }

	result.Issues, err = filterIssues(patchFile, result.Issues)
	if err != nil {
		return err
	}

	for i := range result.Issues {
		if runner.Options.LinterConfig.Output.PrintLinterName {
			result.Issues[i].Text += fmt.Sprintf(" (from %s)", result.Issues[i].FromLinter)
		}

		reviewRequest.Comments = append(reviewRequest.Comments, &github.DraftReviewComment{
			Path:     github.String(result.Issues[i].FilePath()),
			Position: github.Int(result.Issues[i].HunkPos),
			Body:     github.String(result.Issues[i].Text),
		})
	}

	totalComments := len(reviewRequest.Comments)
	runner.Options.Logger.Debug("filtering comments %d", len(reviewRequest.Comments))
	if err := runner.filterComments(&reviewRequest); err != nil {
		return fmt.Errorf("unable to filter comments: %w", err)
	}
	newComments := len(reviewRequest.Comments)
	runner.Options.Logger.Debug("filtered comments down to %d", newComments)

	runner.Options.Logger.Info("golangci-lint reported %d issues (%d issues are new) and %d warnings for %s", totalComments, newComments, len(warnings), runner.meta.Head.FullName)

	passing := false

	if newComments > 0 {
		if totalComments != newComments {
			reviewRequest.Body = github.String(fmt.Sprintf("golangci-lint found %d new issues", newComments))
		} else {
			reviewRequest.Body = github.String(fmt.Sprintf("golangci-lint found %d issues", newComments))
		}
		passing = false
	} else {
		if totalComments != newComments {
			reviewRequest.Body = github.String(runner.Options.NoIssuesText)
		} else {
			reviewRequest.Body = github.String(runner.Options.NoNewIssuesText)
		}
		passing = true
	}

	if len(warnings) > 0 {
		passing = false
		var sb strings.Builder
		if *reviewRequest.Body != "" {
			sb.WriteString(*reviewRequest.Body)
			sb.WriteString(", but ")
		}
		fmt.Fprintf(&sb, "got %d warnings:", len(warnings))
		sb.WriteString("<code>")
		for _, w := range warnings {
			fmt.Fprintf(&sb, "%s: %s\n", w.Tag, strings.TrimSpace(w.Text))
		}
		sb.WriteString("</code>")
		reviewRequest.Body = github.String(sb.String())
	}

	if passing {
		if runner.Options.Approve {
			reviewRequest.Event = github.String(githubEventApprove)
		} else {
			reviewRequest.Event = github.String(githubEventComment)
		}
	} else {
		if runner.Options.RequestChanges {
			reviewRequest.Event = github.String(githubEventRequestChanges)
		} else {
			reviewRequest.Event = github.String(githubEventComment)
		}
	}

	if err := runner.sendReview(&reviewRequest); err != nil {
		return fmt.Errorf("unable to send review: %w", err)
	}
	runner.Options.Logger.Debug("finished with %d, took %s", runner.meta.PullRequestNumber, time.Now().Sub(startTime).String())
	return nil
}

func (runner *Runner) sendReview(reviewRequest *github.PullRequestReviewRequest) error {
	// do not send conditions
	if (*reviewRequest.Event == githubEventRequestChanges || *reviewRequest.Event == githubEventComment) && (reviewRequest.Body == nil || *reviewRequest.Body == "") {
		runner.Options.Logger.Debug("not sending review because body is empty and event is either REQUEST_CHANGES or COMMENT")
		return nil
	}

	if reviewRequest.Body != nil && *reviewRequest.Body == "" {
		reviewRequest.Body = nil
	}

	buf, err := json.Marshal(reviewRequest)
	if err != nil {
		return fmt.Errorf("unable to marshal review: %w", err)
	}
	runner.Options.Logger.Debug("creating review: %s", string(buf))

	if runner.Options.DryRun {
		runner.Options.Logger.Info("aborting creating review because of dry run")
		return nil
	}

	_, _, err = runner.Options.Client.PullRequests.CreateReview(runner.Options.Context, runner.meta.Base.OwnerName, runner.meta.Base.RepoName, runner.meta.PullRequestNumber, reviewRequest)
	if err != nil {
		return fmt.Errorf("unable to create review %s: %w", string(buf), err)
	}
	return nil
}

func (runner *Runner) filterComments(request *github.PullRequestReviewRequest) error {
	page := 1
	for {
		comments, res, err := runner.Options.Client.PullRequests.ListComments(runner.Options.Context, runner.meta.Base.OwnerName, runner.meta.Base.RepoName, runner.meta.PullRequestNumber, &github.PullRequestListCommentsOptions{
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 30,
			},
		})

		if err != nil {
			return err
		}

		for _, comment := range comments {
			for i := len(request.Comments) - 1; i >= 0; i-- {
				if request.Comments[i].GetPosition() != comment.GetPosition() {
					continue
				}
				if request.Comments[i].GetPath() != comment.GetPath() {
					continue
				}
				if request.Comments[i].GetBody() != comment.GetBody() {
					continue
				}
				request.Comments = append(request.Comments[:i], request.Comments[i+1:]...)
				if len(request.Comments) == 0 {
					return nil
				}
			}
		}
		if res.NextPage <= 0 {
			return nil
		}
		page = res.NextPage
	}

	return nil
}

func (runner *Runner) downloadPatch(patchFile string) error {
	runner.Options.Logger.Debug("downloading patch file")
	s, _, err := runner.Options.Client.PullRequests.GetRaw(context.Background(), runner.meta.Base.OwnerName, runner.meta.Base.RepoName, runner.meta.PullRequestNumber, github.RawOptions{github.Diff})
	if err != nil {
		return fmt.Errorf("unable to download patch file: %w", err)
	}

	return ioutil.WriteFile(patchFile, []byte(s), 0744)
}

func (runner *Runner) clone(repoDir string) error {
	branchName := fmt.Sprintf("refs/heads/%s", runner.meta.Head.Ref)
	runner.Options.Logger.Debug("cloning %s (%s) to %s", runner.meta.Head.CloneURL, branchName, repoDir)
	_, err := git.PlainCloneContext(runner.Options.Context, repoDir, false, &git.CloneOptions{
		URL: runner.meta.Head.CloneURL,
		Auth: &gitHttp.BasicAuth{
			// can be anything expect empty
			Username: "x-access-token",
			Password: runner.Options.CloneToken,
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

	runner.meta.PullRequestNumber = runner.Options.PullRequest.GetNumber()
	if runner.meta.PullRequestNumber == 0 {
		return errors.New("unable to get number from pull request")
	}

	runner.meta.PullRequestURL = runner.Options.PullRequest.GetHTMLURL()
	if runner.meta.PullRequestURL == "" {
		return errors.New("unable to get url from pull request")
	}

	var err error
	base := runner.Options.PullRequest.GetBase()
	if base == nil {
		return errors.New("unable to get base")
	}
	runner.meta.Base, err = runner.getBranchMeta(base)
	if err != nil {
		return fmt.Errorf("unable to get branch meta for base: %w", err)
	}

	head := runner.Options.PullRequest.GetHead()
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

func (r *Runner) readRepoConfig(repoDir string) error {
	p := filepath.Join(repoDir, r.Options.LinterConfig.Run.Config)
	r.Options.Logger.Debug("trying to read linter config file %s", p)
	file, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			r.Options.Logger.Debug("no config file present")
			return nil
		}
	}
	defer file.Close()

	var cfg config.Config

	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(file); err != nil {
		return err
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return err
	}

	if err = mergo.Merge(&r.Options.LinterConfig, cfg, mergo.WithOverride); err != nil {
		return nil
	}

	buf, err := json.Marshal(r.Options.LinterConfig)
	if err != nil {
		return err
	}

	r.Options.Logger.Debug("successfully read config %s: %s", p, string(buf))
	return nil
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

func makeInstallationClient(token string) (*github.Client, error) {
	return github.NewClient(&http.Client{Transport: &installationTransport{underlyingTransport: http.DefaultTransport, token: token}}), nil
}
