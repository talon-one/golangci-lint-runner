FROM golang:1.14.2-buster AS BUILDER

COPY . /go/src/github.com/talon-one/golangci-lint-runner

RUN export CGO_ENABLED=0 && \
    go build -o /app/golangci-lint-runner github.com/talon-one/golangci-lint-runner/cmd/golangci-lint-runner


FROM golangci/golangci-lint:v1.23.3

COPY --from=BUILDER /app/golangci-lint-runner /bin/golangci-lint-runner

CMD ["/bin/golangci-lint-runner"]