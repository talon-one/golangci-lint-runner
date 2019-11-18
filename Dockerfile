FROM golang:1.13.4-buster AS BUILDER

COPY . /go/src/github.com/talon-one/golangci-lint-runner

RUN export CGO_ENABLED=0 && \
    go build -o /app/golangci-lint-runner github.com/talon-one/golangci-lint-runner/cmd/golangci-lint-runner


FROM golangci/golangci-lint:v1.21.0

COPY --from=BUILDER /app/golangci-lint-runner /bin/golangci-lint-runner

#CMD ["/app/golangci-lint-runner", "--clone", "--dir=/go/src/github.com/talon-one/talon-service" --github-pull-request-id=3655 --github-repository=talon-one/talon-service --linter=gocyclo --debu]