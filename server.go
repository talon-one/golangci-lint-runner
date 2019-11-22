package golangci_lint_runner

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"time"

	"context"

	"github.com/google/go-github/github"
	"github.com/talon-one/golangci-lint-runner/internal"
)

type Server struct {
	Options *Options
}

type Options struct {
	PrivateKey           *rsa.PrivateKey
	WebhookSecret        string
	webHookSecret        []byte
	AppID                int64
	Logger               Logger
	Timeout              time.Duration
	DefaultLinterOptions LinterOptions
}

type Logger interface {
	Debug(string, ...interface{})
	Error(string, ...interface{})
	Warn(string, ...interface{})
	Info(string, ...interface{})
}

type LinterOptions struct {
	Linters           []string
	IncludeLinterName bool
}

func NewServer(options *Options) (*Server, error) {
	if options == nil {
		return nil, errors.New("Options must be specified")
	}
	if options.PrivateKey == nil {
		return nil, errors.New("PrivateKey must be specified")
	}
	if options.WebhookSecret == "" {
		return nil, errors.New("WebhookSecret must be specified")
	}
	options.webHookSecret = []byte(options.WebhookSecret)
	if options.AppID == 0 {
		return nil, errors.New("AppID must be specified")
	}
	if options.Logger == nil {
		return nil, errors.New("Logger must be specified")
	}
	if options.Timeout <= 0 {
		options.Timeout = time.Minute * 60
	}
	return &Server{
		Options: options,
	}, nil
}

func (srv *Server) HttpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handler(srv.handleEvent))
	return mux
}

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

func (srv *Server) handler(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		w := &Writer{
			ResponseWriter: writer,
			writtenStatus:  false,
		}
		if err := h(w, request); err != nil {
			switch e := err.(type) {
			case internal.WireError:
				if e.StatusCode == 0 {
					e.StatusCode = http.StatusInternalServerError
				}
				if e.PublicError == nil {
					e.PublicError = errors.New("error")
				}
				srv.Options.Logger.Error("%d: %s\n%s", e.StatusCode, e.PublicError, e.PrivateError)

				http.Error(writer, e.PublicError.Error(), e.StatusCode)
				return
			}
			s := err.Error()
			srv.Options.Logger.Error("%d: %s\n", http.StatusInternalServerError, s)
			http.Error(writer, s, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (srv *Server) handleEvent(writer http.ResponseWriter, request *http.Request) error {
	srv.Options.Logger.Debug("got event from %s", request.RemoteAddr)
	payload, err := github.ValidatePayload(request, srv.Options.webHookSecret)
	if err != nil {
		return internal.WireError{
			StatusCode:   http.StatusBadRequest,
			PublicError:  errors.New("unable to validate payload"),
			PrivateError: errors.New("invalid payload"),
		}
	}
	event, err := github.ParseWebHook(github.WebHookType(request), payload)
	switch e := event.(type) {
	case *github.PullRequestEvent:
		return srv.handlePullRequest(writer, request, e)
	case *github.PingEvent, *github.InstallationEvent:
		return nil
	}
	srv.Options.Logger.Warn("unhandled event %T", event)

	return internal.WireError{
		StatusCode:   http.StatusBadRequest,
		PublicError:  errors.New("unknown event"),
		PrivateError: fmt.Errorf("unknown event %T", event),
	}
}

func (srv *Server) handlePullRequest(writer http.ResponseWriter, request *http.Request, event *github.PullRequestEvent) error {
	switch event.GetAction() {
	case "opened", "reopened", "synchronize":
		return srv.handlePullRequestOpened(writer, request, event)
	}
	srv.Options.Logger.Warn("unhandled action %s", event.GetAction())
	return nil
}

func (srv *Server) handlePullRequestOpened(writer http.ResponseWriter, request *http.Request, event *github.PullRequestEvent) error {
	pr := event.GetPullRequest()
	if pr == nil {
		return internal.WireError{
			PrivateError: errors.New("unable to get pull request from event"),
		}
	}

	installation := event.GetInstallation()
	if installation == nil {
		return internal.WireError{
			PrivateError: errors.New("unable to get installation from event"),
		}
	}
	ctx, _ := context.WithTimeout(context.Background(), srv.Options.Timeout)
	runner, err := NewRunner(ctx, installation, pr, srv.Options)
	if err != nil {
		return internal.WireError{
			PublicError:  errors.New("unable to create runner"),
			PrivateError: fmt.Errorf("unable to create runner: %w", err),
		}
	}
	go func() {
		if err := runner.Run(); err != nil {
			srv.Options.Logger.Error("runner failed: %s", err.Error())
		}
	}()
	return nil

	return nil
}
