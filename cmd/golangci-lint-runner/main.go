package main

import (
	"fmt"
	"log"
	"net/http"

	"os"

	"io/ioutil"

	"github.com/dgrijalva/jwt-go"
	golangci_lint_runner "github.com/talon-one/golangci-lint-runner"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	addrFlag           = kingpin.Flag("host-addr", "address to listen to, if unspecified takes HOST_ADDR environment variable").Envar("HOST_ADDR").Required().String()
	privateKeyFlag     = kingpin.Flag("private-key", "github private key").Envar("GITHUB_PRIVATE_KEY").Required().ExistingFile()
	webhookSecretFlag  = kingpin.Flag("webhook-secret", "github webhook secret").Envar("GITHUB_WEBHOOK_SECRET").Required().String()
	appIdFlag          = kingpin.Flag("appid", "github app id").Envar("GITHUB_APP_ID").Required().Int64()
	queueSizeFlag      = kingpin.Flag("queue-size", "queue size").Envar("QUEUE_SIZE").Default("100").Int()
	cacheDirFlag       = kingpin.Flag("cache-dir", "cache dir").Envar("CACHE_DIR").String()
	approveFlag        = kingpin.Flag("approve", "whether the app should approve if no issues were found (selecting false will only result in a comment)").Envar("APPROVE").Bool()
	requestChangesFlag = kingpin.Flag("request-changes", "whether the app should request changes if issues were found (selecting false will only result in a comment)").Envar("REQUEST_CHANGES").Bool()
	debugFlag          = kingpin.Flag("debug", "enable debug log").Envar("DEBUG").Hidden().Bool()
	dryRunFlag         = kingpin.Flag("dry-run", "do not actual post on the pr").Envar("DRY_RUN").Bool()
)
var version string
var commit string
var date string

func main() {
	kingpin.Version(fmt.Sprintf("%s %s %s", version, commit, date))
	kingpin.Parse()

	logger := logger{}

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

	options := golangci_lint_runner.Options{
		PrivateKey:     privateKey,
		WebhookSecret:  *webhookSecretFlag,
		AppID:          *appIdFlag,
		Logger:         logger,
		Timeout:        0,
		CacheDir:       *cacheDirFlag,
		Approve:        *approveFlag,
		RequestChanges: *requestChangesFlag,
		DryRun:         *dryRunFlag,
		DefaultLinterOptions: golangci_lint_runner.LinterOptions{
			Linters:           []string{"deadcode", "errcheck", "gocritic", "gocyclo", "goimports", "golint", "gosimple", "govet", "ineffassign", "misspell", "nakedret", "prealloc", "staticcheck", "structcheck", "typecheck", "unconvert", "unparam", "unused", "varcheck"},
			IncludeLinterName: true,
		},
	}

	if options.CacheDir == "" {
		options.CacheDir, err = ioutil.TempDir("", "golangci-lint-server-cache")
		if err != nil {
			logger.Error("could not create cache dir: %s", err)
			os.Exit(1)
		}
	}

	if *queueSizeFlag <= 0 {
		logger.Error("could not use a queue <= 0")
		os.Exit(1)
	}

	srv, err := golangci_lint_runner.NewServer(*queueSizeFlag, &options)
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
