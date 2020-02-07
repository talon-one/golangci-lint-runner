# golangci-lint-runner

An runner for [golangci-lint](github.com/golangci/golangci-lint) with an default (overrideable) config in an CI environment. (for github)

## Features
* Comment, Approve or Reject Pull Requests
* Custom or multiple [.golangci.yml](https://github.com/golangci/golangci-lint/blob/master/.golangci.example.yml) files.
* Use as an github app with hooks, standalone (triggered by CI or manually) or .github actions

## Github Actions Setup
Create a workflow file (e.g. `.github/workflows/golangci-lint-runner.yml`):
```yml
name: golangci-lint-runner
on: pull_request
jobs:
  golangci-lint-runner:
    runs-on: ubuntu-latest
    steps:
      - name: linter
        uses: "docker://talononedevs/golangci-lint-runner@sha256:2e00aaabc35fe2253e78ec593f96b3a787081007fdcccc0a633ab1f46375dab0"
        with:
          entrypoint: /bin/golangci-lint-runner
          args: standalone
        env:
          APPROVE: false
          REQUEST_CHANGES: false
          GITHUB_TOKEN: "${{ secrets.GITHUB_TOKEN }}"
``` 

## Github App Setup
1. Create an Github App
1. Deploy golangci-lint-runner as app somewhere
1. set Homepage and Webhook url to your deployment
1. create a private key and pass it into golangci-lint-runner
1. create a webhook secret and pass it into golangci-lint-runner
1. Subscribe to events: `Pull request`
1. Permissions to set:
```
Contents: Read-Only
Pull-Request: Read & write
Metadata: Read-Only
```


> Note: The code quality is not the best, this was done in a short period of time
> There is a lot to improve e.g. tests...