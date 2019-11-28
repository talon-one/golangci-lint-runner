package main

import (
	"fmt"
	"log"
	"net/http"

	"os"

	"io/ioutil"

	"context"

	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/google/go-github/github"
	golangci_lint_runner "github.com/talon-one/golangci-lint-runner"
	"golang.org/x/oauth2"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	cacheDirFlag       = kingpin.Flag("cache-dir", "cache dir").Envar("CACHE_DIR").String()
	approveFlag        = kingpin.Flag("approve", "whether the app should approve if no issues were found (selecting false will only result in a comment)").Envar("APPROVE").Bool()
	requestChangesFlag = kingpin.Flag("request-changes", "whether the app should request changes if issues were found (selecting false will only result in a comment)").Envar("REQUEST_CHANGES").Bool()
	debugFlag          = kingpin.Flag("debug", "enable debug log").Envar("DEBUG").Hidden().Bool()
	dryRunFlag         = kingpin.Flag("dry-run", "do not actual post on the pr").Envar("DRY_RUN").Bool()

	appCmd            = kingpin.Command("app", "run as an app")
	addrFlag          = appCmd.Flag("host-addr", "address to listen to, if unspecified takes HOST_ADDR environment variable").Envar("HOST_ADDR").Required().String()
	privateKeyFlag    = appCmd.Flag("private-key", "github private key").Envar("GITHUB_PRIVATE_KEY").Required().ExistingFile()
	webhookSecretFlag = appCmd.Flag("webhook-secret", "github webhook secret").Envar("GITHUB_WEBHOOK_SECRET").Required().String()
	appIdFlag         = appCmd.Flag("appid", "github app id").Envar("GITHUB_APP_ID").Required().Int64()
	queueSizeFlag     = appCmd.Flag("queue-size", "queue size").Envar("QUEUE_SIZE").Default("100").Int()

	standAloneCmd         = kingpin.Command("standalone", "run standalone")
	tokenFlag             = standAloneCmd.Flag("token", "github token to use").Envar("GITHUB_TOKEN").Required().String()
	pullRequestNumberFlag = standAloneCmd.Flag("pull-request-number", "github pull request number").Envar("GITHUB_PULL_REQUEST_NUMBER").Required().Int()
	repoNameFlag          = standAloneCmd.Flag("repo-name", "github repository name").Envar("GITHUB_REPO_NAME").Required().String()
	repoOwnerFlag         = standAloneCmd.Flag("repo-owner", "github repository owner").Envar("GITHUB_REPO_OWNER").Required().String()
)
var version string
var commit string
var date string

func main() {
	kingpin.Version(fmt.Sprintf("%s %s %s", version, commit, date))
	switch kingpin.Parse() {
	case appCmd.FullCommand():
		server()
	case standAloneCmd.FullCommand():
		standalone()
	}
}

func options(logger logger) *golangci_lint_runner.Options {
	var err error
	options := golangci_lint_runner.Options{
		Logger:         logger,
		Timeout:        0,
		CacheDir:       *cacheDirFlag,
		Approve:        *approveFlag,
		RequestChanges: *requestChangesFlag,
		DryRun:         *dryRunFlag,
		LinterOptions: golangci_lint_runner.LinterOptions{
			Linters:           []string{"deadcode", "errcheck", "gocritic", "gocyclo", "goimports", "golint", "gosimple", "govet", "ineffassign", "misspell", "nakedret", "prealloc", "staticcheck", "structcheck", "typecheck", "unconvert", "unparam", "unused", "varcheck"},
			IncludeLinterName: true,
		},
	}

	if options.Timeout <= 0 {
		options.Timeout = time.Minute * 10
	}

	if options.CacheDir == "" {
		options.CacheDir, err = ioutil.TempDir("", "golangci-lint-runner-cache-")
		if err != nil {
			logger.Error("could not create cache dir: %s", err)
			os.Exit(1)
		}
	}

	if options.DryRun {
		options.Logger.Debug("running in dry mode")
	}
	return &options
}

func server() {
	logger := logger{}
	logger.Debug("running in server mode")
	// read private key
	privateKeyBytes, err := ioutil.ReadFile(*privateKeyFlag)
	if err != nil {
		logger.Error("could not read private key: %s", err)
		os.Exit(1)
	}

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(privateKeyBytes)
	if err != nil {
		logger.Error("could not parse private key: %s", err)
		os.Exit(1)
	}

	options := golangci_lint_runner.ServerOptions{
		PrivateKey:    privateKey,
		WebhookSecret: *webhookSecretFlag,
		AppID:         *appIdFlag,
		QueueSize:     *queueSizeFlag,
		Options:       options(logger),
	}

	if options.QueueSize <= 0 {
		logger.Error("could not use a queue <= 0")
		os.Exit(1)
	}

	srv, err := golangci_lint_runner.NewServer(&options)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	defer srv.Close()

	logger.Info("Stating listening on %s", *addrFlag)
	if err := http.ListenAndServe(*addrFlag, srv.HttpHandler()); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func standalone() {
	logger := logger{}
	logger.Debug("running in standalone mode")
	opt := options(logger)
	opt.Context, _ = context.WithTimeout(context.Background(), opt.Timeout)
	opt.PullRequestNumber = *pullRequestNumberFlag
	opt.Owner = *repoOwnerFlag
	opt.Name = *repoNameFlag
	opt.CloneToken = *tokenFlag

	opt.Client = github.NewClient(oauth2.NewClient(opt.Context, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: *tokenFlag},
	)))

	runner, err := golangci_lint_runner.NewRunner(*opt)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	if err = runner.Run(); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

type logger struct{}

func (logger) Debug(format string, a ...interface{}) {
	if debugFlag == nil || !*debugFlag {
		return
	}
	log.Println(fmt.Sprintf("[DEBUG] "+format, a...))
}

func (logger) Info(format string, a ...interface{}) {
	log.Println(fmt.Sprintf(format, a...))
}

func (logger) Warn(format string, a ...interface{}) {
	log.Println(fmt.Sprintf("Warning: "+format, a...))
}

func (logger) Error(format string, a ...interface{}) {
	log.Println(fmt.Sprintf("Error: "+format, a...))
}
