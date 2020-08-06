package main

import (
	"fmt"
	"log"
	"net/http"

	"os"

	"io/ioutil"

	"context"

	"time"

	"strings"

	"github.com/dgrijalva/jwt-go"
	"github.com/golangci/golangci-lint/pkg/config"
	"github.com/google/go-github/github"
	golangci_lint_runner "github.com/talon-one/golangci-lint-runner"
	"github.com/valyala/fastjson"
	"golang.org/x/oauth2"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	cacheDirFlag        = kingpin.Flag("cache-dir", "cache dir").Envar("CACHE_DIR").String()
	approveFlag         = kingpin.Flag("approve", "whether the app should approve if no issues were found (selecting false will only result in a comment)").Envar("APPROVE").Bool()
	requestChangesFlag  = kingpin.Flag("request-changes", "whether the bot should request changes if issues were found (selecting false will only result in a comment)").Envar("REQUEST_CHANGES").Bool()
	noChangesTextFlag   = kingpin.Flag("no-changes-text", "the text the bot should send if there are no go code changes").Envar("NO_CHANGES_TEXT").Default().String()
	noIssuesTextFlag    = kingpin.Flag("no-issues-text", "the text the bot should send if there are no issues").Envar("NO_ISSUES_TEXT").Default("").String()
	noNewIssuesTextFlag = kingpin.Flag("no-new-issues-text", "the text the bot should send if there are no new issues").Envar("NO_NEW_ISSUES_TEXT").Default("").String()
	configFileFlag      = kingpin.Flag("config", "which config file to use").Envar("CONFIG_FILE").Default(".golangci.yml").String()
	debugFlag           = kingpin.Flag("debug", "enable debug log").Envar("DEBUG").Hidden().Bool()
	dryRunFlag          = kingpin.Flag("dry-run", "do not actual post on the pr").Envar("DRY_RUN").Bool()

	appCmd            = kingpin.Command("app", "run as an app")
	addrFlag          = appCmd.Flag("host-addr", "address to listen to, if unspecified takes HOST_ADDR environment variable").Envar("HOST_ADDR").Required().String()
	privateKeyFlag    = appCmd.Flag("private-key", "github private key").Envar("GITHUB_PRIVATE_KEY").Required().ExistingFile()
	webhookSecretFlag = appCmd.Flag("webhook-secret", "github webhook secret").Envar("GITHUB_WEBHOOK_SECRET").Required().String()
	appIdFlag         = appCmd.Flag("appid", "github app id").Envar("GITHUB_APP_ID").Required().Int64()
	queueSizeFlag     = appCmd.Flag("queue-size", "queue size").Envar("QUEUE_SIZE").Default("100").Int()

	standAloneCmd         = kingpin.Command("standalone", "run standalone")
	tokenFlag             = standAloneCmd.Flag("token", "github token to use").Envar("GITHUB_TOKEN").Required().String()
	pullRequestNumberFlag = standAloneCmd.Flag("pull-request-number", "github pull request number").Envar("GITHUB_PULL_REQUEST_NUMBER").Int()
	repoNameFlag          = standAloneCmd.Flag("repo-name", "github repository name").Envar("GITHUB_REPO_NAME").String()
	repoOwnerFlag         = standAloneCmd.Flag("repo-owner", "github repository owner").Envar("GITHUB_REPO_OWNER").String()
	repoRepoFlag          = standAloneCmd.Flag("repo", "github full repository").Envar("GITHUB_REPOSITORY").String()
	eventPath             = standAloneCmd.Flag("event-path", "the event path (only github actions)").Envar("GITHUB_EVENT_PATH").String()
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

	config := config.Config{
		Run: config.Run{
			Config: *configFileFlag,
		},
		Output: struct {
			Format              string
			Color               string
			PrintIssuedLine     bool   `mapstructure:"print-issued-lines"`
			PrintLinterName     bool   `mapstructure:"print-linter-name"`
			UniqByLine          bool   `mapstructure:"uniq-by-line"`
			SortResults         bool   `mapstructure:"sort-results"`
			PrintWelcomeMessage bool   `mapstructure:"print-welcome"`
			PathPrefix          string `mapstructure:"path-prefix"`
		}{
			PrintLinterName: true,
		},
		LintersSettings: config.LintersSettings{
			Errcheck: config.ErrcheckSettings{
				CheckTypeAssertions: false,
				CheckAssignToBlank:  false,
			},
			Funlen: struct {
				Lines      int
				Statements int
			}{
				Lines:      60,
				Statements: 40,
			},
			Lll: config.LllSettings{
				LineLength: 120,
				TabWidth:   1,
			},
			Govet: config.GovetSettings{
				CheckShadowing: false,
				Settings:       nil,
				Enable: []string{
					"asmdecl",
					"assign",
					"atomic",
					"bools",
					"buildtag",
					"cgocall",
					"composites",
					"copylocks",
					"httpresponse",
					"loopclosure",
					"lostcancel",
					"nilfunc",
					"printf",
					"shift",
					"stdmethods",
					"structtag",
					"tests",
					"unmarshal",
					"unreachable",
					"unsafeptr",
					"unusedresult",
				},
				Disable:    []string{"unreachable"},
				EnableAll:  false,
				DisableAll: false,
			},
			Golint: struct {
				MinConfidence float64 `mapstructure:"min-confidence"`
			}{
				MinConfidence: 0.8,
			},
			Gofmt: struct{ Simplify bool }{Simplify: true},
			Gocyclo: struct {
				MinComplexity int `mapstructure:"min-complexity"`
			}{
				MinComplexity: 20,
			},
			Unparam: config.UnparamSettings{
				CheckExported: true,
				Algo:          "cha",
			},
			Nakedret: config.NakedretSettings{
				MaxFuncLines: 30,
			},
			Prealloc: config.PreallocSettings{
				Simple:     true,
				RangeLoops: true,
				ForLoops:   false,
			},
			Gocritic: config.GocriticSettings{
				EnabledChecks: []string{
					"assignOp",
					"captLocal",
					"defaultCaseOrder",
					"elseif",
					"ifElseChain",
					"regexpMust",
					"singleCaseSwitch",
					"sloppyLen",
					"switchTrue",
					"typeSwitchVar",
					"underef",
					"unlambda",
					"unslice",
				},
				SettingsPerCheck: map[string]config.GocriticCheckSettings{},
			},
			Godox: config.GodoxSettings{
				Keywords: []string{"TODO", "BUG", "FIXME"},
			},
			Dogsled: config.DogsledSettings{
				MaxBlankIdentifiers: 2,
			},
			Gocognit: config.GocognitSettings{
				MinComplexity: 20,
			},
			Maligned: struct {
				SuggestNewOrder bool `mapstructure:"suggest-new"`
			}{
				SuggestNewOrder: true,
			},
			Dupl: struct{ Threshold int }{Threshold: 100},
			Goconst: struct {
				MinStringLen        int `mapstructure:"min-len"`
				MinOccurrencesCount int `mapstructure:"min-occurrences"`
			}{
				MinStringLen:        3,
				MinOccurrencesCount: 3,
			},
			Misspell: struct {
				Locale      string
				IgnoreWords []string `mapstructure:"ignore-words"`
			}{
				Locale:      "US",
				IgnoreWords: nil,
			},
			Unused: struct {
				CheckExported bool `mapstructure:"check-exported"`
			}{
				CheckExported: true,
			},
			Whitespace: struct {
				MultiIf   bool `mapstructure:"multi-if"`
				MultiFunc bool `mapstructure:"multi-func"`
			}{
				MultiIf:   false,
				MultiFunc: false,
			},
			WSL: config.WSLSettings{
				StrictAppend:                     true,
				AllowAssignAndCallCuddle:         true,
				AllowMultiLineAssignCuddle:       true,
				AllowCuddleDeclaration:           false,
				AllowTrailingComment:             false,
				ForceCaseTrailingWhitespaceLimit: 0,
			},
		},
		Linters: config.Linters{
			Enable:     []string{"deadcode", "errcheck", "gocritic", "gocyclo", "goimports", "golint", "gosimple", "govet", "ineffassign", "misspell", "nakedret", "prealloc", "staticcheck", "structcheck", "typecheck", "unconvert", "unparam", "unused", "varcheck"},
			Disable:    nil,
			EnableAll:  false,
			DisableAll: true,
			Fast:       false,
			Presets:    nil,
		},
	}

	options := golangci_lint_runner.Options{
		Logger:          logger,
		Timeout:         0,
		CacheDir:        *cacheDirFlag,
		Approve:         *approveFlag,
		RequestChanges:  *requestChangesFlag,
		DryRun:          *dryRunFlag,
		LinterConfig:    config,
		NoChangesText:   *noChangesTextFlag,
		NoIssuesText:    *noIssuesTextFlag,
		NoNewIssuesText: *noNewIssuesTextFlag,
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

	if *repoNameFlag == "" && *repoOwnerFlag == "" && *repoRepoFlag == "" {
		logger.Error("must either specify --repo or --repo-name + --repo-owner")
		os.Exit(1)
	}

	if *repoRepoFlag != "" {
		parts := strings.FieldsFunc(*repoRepoFlag, func(r rune) bool {
			return r == '/'
		})
		if len(parts) != 2 {
			logger.Error("unable to parse %s", *repoRepoFlag)
			os.Exit(1)
		}
		repoOwnerFlag = &parts[0]
		repoNameFlag = &parts[1]
	}

	if *eventPath != "" {
		b, err := ioutil.ReadFile(*eventPath)
		if err != nil {
			logger.Error("unable to open %s: %s", *eventPath, err)
			os.Exit(1)
		}
		id := fastjson.GetInt(b, "pull_request", "number")
		if id == 0 {
			logger.Error("unable to get pull_request.number from %s: %s", *eventPath, string(b))
			os.Exit(1)
		}
		pullRequestNumberFlag = &id
	}

	if *pullRequestNumberFlag == 0 {
		logger.Error("must either specify --event-path or --pull-request-number")
		os.Exit(1)
	}

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
