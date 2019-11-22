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
	addrFlag          = kingpin.Flag("host-addr", "address to listen to, if unspecified takes HOST_ADDR environment variable").Envar("HOST_ADDR").Required().String()
	privateKeyFlag    = kingpin.Flag("private-key", "github private key").Envar("GITHUB_PRIVATE_KEY").Required().ExistingFile()
	webhookSecretFlag = kingpin.Flag("webhook-secret", "github webhook secret").Envar("GITHUB_WEBHOOK_SECRET").Required().String()
	appIdFlag         = kingpin.Flag("appid", "github app id").Envar("GITHUB_APP_ID").Required().Int64()
	debugFlag         = kingpin.Flag("debug", "enable debug log").Envar("DEBUG").Hidden().Bool()
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
		PrivateKey:    privateKey,
		WebhookSecret: *webhookSecretFlag,
		AppID:         *appIdFlag,
		Logger:        logger,
		Timeout:       0,
		DefaultLinterOptions: golangci_lint_runner.LinterOptions{
			Linters:           []string{"deadcode", "errcheck", "gosimple", "govet", "ineffassign", "misspell", "staticcheck", "structcheck", "typecheck", "unused", "varcheck"},
			IncludeLinterName: true,
		},
	}

	srv, err := golangci_lint_runner.NewServer(&options)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	logger.Info("Stating listening on %s\n", *addrFlag)
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
