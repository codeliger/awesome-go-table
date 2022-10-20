package main

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/google/go-github/github"
	"github.com/migueleliasweb/go-github-mock/src/mock"
)

func mockHTTPClient() *github.Client {
	contributers := []*github.Contributor{}

	randomNumber := rand.Intn(100) + 1

	for i := 0; i < randomNumber; i++ {
		contributers = append(contributers, &github.Contributor{
			Login:         github.String("user" + fmt.Sprint(i)),
			Contributions: github.Int(1),
		})
	}

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetReposByOwnerByRepo,
			github.Repository{
				Name:            github.String("foobar"),
				StargazersCount: github.Int(100),
				UpdatedAt:       &github.Timestamp{Time: time.Now()},
			},
		),
		mock.WithRequestMatch(
			mock.GetReposContributorsByOwnerByRepo,
			contributers,
		),
		mock.WithRequestMatchHandler(
			mock.GetReposByOwnerByRepo,
			func() (interface{}, error) {
				return nil, fmt.Errorf("error")
			}
		),
		)
		mock.WithRequestMatch(
			mock.GetRateLimit,
			github.Rate{
				Limit:     5000,
				Remaining: 40,
				Reset:     github.Timestamp{Time: time.Now().Add(time.Hour)},
			},
		),
	)

	return github.NewClient(mockedHTTPClient)
}
