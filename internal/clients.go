package internal

import (
	"net/http"
	"strconv"
	"time"

	"crypto/rsa"

	"github.com/DataDog/ghinstallation"
	"github.com/dgrijalva/jwt-go"
	"github.com/google/go-github/github"
)

type transport struct {
	underlyingTransport http.RoundTripper
	token               string
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Add("Authorization", "Bearer "+t.token)
	return t.underlyingTransport.RoundTrip(req)
}

func MakeClients(appID int64, installationID int64, privateKey *rsa.PrivateKey) (*github.Client, *github.Client, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": time.Now().Unix(),
		"exp": time.Now().Local().Add(time.Minute * 5).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})

	tokenString, err := token.SignedString(privateKey)
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
