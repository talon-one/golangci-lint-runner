package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"io/ioutil"

	"errors"

	"strconv"
	"time"

	"github.com/DataDog/ghinstallation"
	"github.com/dgrijalva/jwt-go"
	"github.com/google/go-github/github"
	golangci_lint_runner "github.com/talon-one/golangci-lint-runner"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	addrFlag          = kingpin.Flag("host-addr", "address to listen to, if unspecified takes HOST_ADDR environment variable").Envar("HOST_ADDR").Required().String()
	privateKeyFlag    = kingpin.Flag("private-key", "github private key").Envar("GITHUB_PRIVATE_KEY").Required().ExistingFile()
	webhookSecretFlag = kingpin.Flag("webhook-secret", "github webhook secret").Envar("GITHUB_WEBHOOK_SECRET").Required().String()
	appIdFlag         = kingpin.Flag("appid", "github app id").Envar("GITHUB_APP_ID").Required().Int64()
	debugFlag         = kingpin.Flag("debug", "enable debug log").Hidden().Bool()
)
var version string
var commit string
var date string

type Logger interface {
	Printf(string, ...interface{})
}

var debug Logger

func main() {
	kingpin.Version(fmt.Sprintf("%s %s %s", version, commit, date))
	kingpin.Parse()

	// enable debug
	if debugFlag != nil && *debugFlag {
		debug = debugLogger{}
	} else {
		debug = dummyLogger{}
	}

	http.HandleFunc("/", handler(handleEvent))
	debug.Printf("Stating listening on %s\n", *addrFlag)
	if err := http.ListenAndServe(*addrFlag, nil); err != nil {
		log.Fatal(err)
	}
}

type debugLogger struct{}

func (debugLogger) Printf(format string, a ...interface{}) {
	log.Printf(format, a...)
}

type dummyLogger struct{}

func (dummyLogger) Printf(string, ...interface{}) {}

type WireError struct {
	StatusCode   int
	PublicError  error
	PrivateError error
}

func (WireError) Error() string { return "" }

type Writer struct {
	http.ResponseWriter
	writtenStatus bool
}

func (w *Writer) WriteHeader(statusCode int) {
	if w.writtenStatus {
		return
	}
	w.writtenStatus = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func handler(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		w := &Writer{
			ResponseWriter: writer,
			writtenStatus:  false,
		}
		if err := h(w, request); err != nil {
			switch e := err.(type) {
			case WireError:
				if e.StatusCode == 0 {
					e.StatusCode = http.StatusInternalServerError
				}
				if e.PublicError == nil {
					e.PublicError = errors.New("error")
				}
				log.Printf("Error %d: %s\n             %s\n", e.StatusCode, e.PublicError, e.PrivateError)

				http.Error(writer, e.PublicError.Error(), e.StatusCode)
				return

			}
			s := err.Error()
			log.Printf("Error %d: %s\n", http.StatusInternalServerError, s)
			http.Error(writer, s, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleEvent(writer http.ResponseWriter, request *http.Request) error {
	debug.Printf("got event from %s", request.RemoteAddr)
	payload, err := github.ValidatePayload(request, []byte(*webhookSecretFlag))
	if err != nil {
		return WireError{
			StatusCode:   http.StatusBadRequest,
			PublicError:  errors.New("unable to validate payload"),
			PrivateError: errors.New("invalid payload"),
		}
	}
	event, err := github.ParseWebHook(github.WebHookType(request), payload)
	switch e := event.(type) {
	case *github.PullRequestEvent:
		return handlePullRequest(writer, request, e)
	}
	debug.Printf("unhandled event %T", event)

	return nil
}

func handlePullRequest(writer http.ResponseWriter, request *http.Request, event *github.PullRequestEvent) error {
	switch event.GetAction() {
	case "opened", "reopened", "synchronize":
		return handlePullRequestOpened(writer, request, event)
	}
	debug.Printf("unhandled action %s", event.GetAction())
	return nil
}

type transport struct {
	underlyingTransport http.RoundTripper
	token               string
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Add("Authorization", "Bearer "+t.token)
	return t.underlyingTransport.RoundTrip(req)
}

func makeClients(appID int64, installationID int64) (*github.Client, *github.Client, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": time.Now().Unix(),
		"exp": time.Now().Local().Add(time.Minute * 5).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})

	privateKey, err := ioutil.ReadFile(*privateKeyFlag)
	if err != nil {
		return nil, nil, err
	}

	signKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKey))
	if err != nil {
		return nil, nil, err
	}

	tokenString, err := token.SignedString(signKey)
	if err != nil {
		return nil, nil, err
	}
	appClient := github.NewClient(&http.Client{Transport: &transport{underlyingTransport: http.DefaultTransport, token: tokenString}})

	itr, err := ghinstallation.New(http.DefaultTransport, int(appID), int(installationID), privateKey)
	if err != nil {
		return nil, nil, err
	}
	return appClient, github.NewClient(&http.Client{Transport: itr}), nil
}

func handlePullRequestOpened(writer http.ResponseWriter, request *http.Request, event *github.PullRequestEvent) error {
	pr := event.GetPullRequest()
	i := event.GetInstallation()

	appClient, repoClient, err := makeClients(*appIdFlag, i.GetID())
	if err != nil {
		return WireError{
			PrivateError: fmt.Errorf("unable to make client: %w", err),
		}
	}

	debug.Printf("creating installation token\n")

	itoken, response, err := appClient.Apps.CreateInstallationToken(context.Background(), i.GetID())
	if err != nil {
		return WireError{
			PrivateError: fmt.Errorf("unable to create installation token: %w", err),
		}
	}
	if response.StatusCode != http.StatusCreated {
		return WireError{
			PrivateError: fmt.Errorf("expected 201 but got %d", response.StatusCode),
		}
	}

	r := golangci_lint_runner.Runner{
		PullRequest:       pr,
		CloneToken:        itoken.GetToken(),
		GithubClient:      repoClient,
		Logger:            debug,
		Context:           context.Background(),
		IncludeLinterName: true,
		Timeout:           time.Minute * 10,
		Linters:           []string{"deadcode", "errcheck", "gosimple", "govet", "ineffassign", "misspell", "staticcheck", "structcheck", "typecheck", "unused", "varcheck"},
	}

	if err := r.Run(); err != nil {
		return WireError{
			PublicError:  errors.New("golangci-linter failed"),
			PrivateError: fmt.Errorf("golangci-linter failed: %w", err),
		}
	}
	return nil
}
