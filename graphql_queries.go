package main

import (
	"context"
	"net/http"

	graphql "github.com/hasura/go-graphql-client"
)

type RepositoryOwner struct {
	Login string `graphql:"login" json:"login"`
}

type RepositoryWatchers struct {
	TotalCount int `graphql:"totalCount" json:"totalCount"`
}

type RepositoryIssues struct {
	TotalCount int `graphql:"totalCount" json:"totalCount"`
}

type RepositoryLicense struct {
	Name string `graphql:"name" json:"name"`
}

type GitHubRepository struct {
	Name           string             `graphql:"name" json:"name"`
	Owner          RepositoryOwner    `graphql:"owner" json:"owner"`
	StargazerCount int                `graphql:"stargazerCount" json:"stargazerCount"`
	Watchers       RepositoryWatchers `graphql:"watchers" json:"watchers"`
	ForkCount      int                `graphql:"forkCount" json:"forkCount"`
	CreatedAt      string             `graphql:"createdAt" json:"createdAt"`
	UpdatedAt      string             `graphql:"updatedAt" json:"updatedAt"`
	PushedAt       string             `graphql:"pushedAt" json:"pushedAt"`
	Issues         RepositoryIssues   `graphql:"issues(states: OPEN)" json:"issues"`
	LicenseInfo    *RepositoryLicense `graphql:"licenseInfo" json:"licenseInfo"`
	IsArchived     bool               `graphql:"isArchived" json:"isArchived"`
}

type RepositoryQuery struct {
	Repository *GitHubRepository `graphql:"repository(owner: $owner, name: $name)"`
}

type GitHubRateLimit struct {
	Cost      int    `graphql:"cost" json:"cost"`
	Remaining int    `graphql:"remaining" json:"remaining"`
	ResetAt   string `graphql:"resetAt" json:"resetAt"`
}

type RateLimitQuery struct {
	RateLimit GitHubRateLimit `graphql:"rateLimit"`
}

func GetRepositoryData(client *graphql.Client, owner string, name string) (*GitHubRepository, error) {
	var query RepositoryQuery
	variables := map[string]any{
		"owner": owner,
		"name":  name,
	}

	err := client.Query(context.Background(), &query, variables)
	if err != nil {
		return nil, err
	}

	return query.Repository, nil
}

func GetRateLimit(client *graphql.Client) (*GitHubRateLimit, error) {
	var query RateLimitQuery

	err := client.Query(context.Background(), &query, nil)
	if err != nil {
		return nil, err
	}

	return &query.RateLimit, nil
}

func NewGitHubGraphQLClient(token string) *graphql.Client {
	return graphql.NewClient(
		"https://api.github.com/graphql",
		&authenticatedHTTPClient{token: token},
	)
}

type authenticatedHTTPClient struct {
	token string
}

func (c *authenticatedHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}
