package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

const SPECIALCATEGORY = "#### "
const SUBCATEGORY = "### "
const CATEGORY = "## "

type MarkdownRepo struct {
	Category        string
	Subcategory     string
	SpecialCategory string
	ProjectName     string
	Description     string
	OwnerName       string
	RepoName        string
	GithubPagesName string
	URL             string
}

type GithubRepo struct {
	MarkdownRepo
	Stars            int
	LastCommit       time.Time
	ContributerCount int
	Contributers     map[string]int
	Error            error `json:"omitempty"`
}

func parseCategory(lineGroup []string, category string, subCategory string, specialCategory string) []MarkdownRepo {
	items := []MarkdownRepo{}

	if category == "" {
		fmt.Println("Category is empty")
	}

	if len(lineGroup) == 0 {
		return items
	}

	text := strings.Join(lineGroup, "\n")

	submatches := parseGithubsURLRegex(text)
	for _, match := range submatches {
		item := MarkdownRepo{
			ProjectName:     string(match[1]),
			URL:             string(match[2]),
			Description:     string(match[6]),
			Category:        category,
			Subcategory:     subCategory,
			SpecialCategory: specialCategory,
		}

		if string(match[3]) != "" {
			item.GithubPagesName = string(match[3])
		} else {
			item.OwnerName = string(match[4])
			item.RepoName = string(match[5])
		}

		items = append(items, item)
	}

	return items
}

func getFileName() string {
	return "github_repos.json"
}

func jsonFileExists() bool {
	fileName := getFileName()
	if _, err := os.Stat(fileName); err == nil {
		return false
	}
	return true
}

func reposToJson(githubRepos []GithubRepo) ([]byte, error) {
	bytes, err := json.Marshal(githubRepos)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

func bytesToFile(bytes []byte) error {
	fileName := getFileName()
	err := os.WriteFile(fileName, bytes, 0644)
	if err != nil {
		return err
	}

	return nil
}

func parseMarkdownRepos(githubToken string) ([]MarkdownRepo, error) {
	if !jsonFileExists() {
		fmt.Println("File already exists")
		return nil, nil
	}

	readmeURL := "https://raw.githubusercontent.com/avelino/awesome-go/master/README.md"

	readme := getText(readmeURL)

	lines := strings.Split(readme, "\n")

	specialCategory := ""
	subCategory := ""
	category := ""

	lineGroup := []string{}

	markdownRepos := []MarkdownRepo{}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, SPECIALCATEGORY):
			markdownRepos = append(markdownRepos, parseCategory(lineGroup, category, subCategory, specialCategory)...)
			lineGroup = []string{}
			specialCategory = line[len(SPECIALCATEGORY):]
		case strings.HasPrefix(line, SUBCATEGORY):
			markdownRepos = append(markdownRepos, parseCategory(lineGroup, category, subCategory, specialCategory)...)
			lineGroup = []string{}
			subCategory = line[len(SUBCATEGORY):]
			specialCategory = ""
		case strings.HasPrefix(line, CATEGORY):
			markdownRepos = append(markdownRepos, parseCategory(lineGroup, category, subCategory, specialCategory)...)
			lineGroup = []string{}
			category = line[len(CATEGORY):]
			subCategory = ""
			specialCategory = ""
		default:
			lineGroup = append(lineGroup, line)
		}
	}

	return markdownRepos, nil
}

func getGithubReposFromMarkdownRepos(client *github.Client, markdownRepos []MarkdownRepo) []GithubRepo {
	githubRepoWithContributors := make(chan GithubRepo, len(markdownRepos))

	manageGoRoutines(client, githubRepoWithContributors, markdownRepos)

	githubReposWithContributers := make([]GithubRepo, 0, len(githubRepoWithContributors))

	for githubRepo := range githubRepoWithContributors {
		if githubRepo.Error != nil {
			fmt.Println("Error", githubRepo.Error)
		} else {
			githubReposWithContributers = append(githubReposWithContributers, githubRepo)
		}
	}

	return githubReposWithContributers
}

func manageGoRoutines(client *github.Client, githubRepoWithContributors chan GithubRepo, markdownRepos []MarkdownRepo) {
	markdownRepoChan := make(chan MarkdownRepo, len(markdownRepos))
	githubRepoChan := make(chan GithubRepo, len(markdownRepos))

	for _, item := range markdownRepos {
		markdownRepoChan <- item
	}

	rateLimit := &atomic.Int32{}

	limits, _, err := client.RateLimits(context.Background())
	if err != nil {
		panic(fmt.Sprintf("Error getting rate limits: %v", err))
	}

	rateLimit.Store(int32(limits.Core.Remaining))

	wg := sync.WaitGroup{}

	const THREADS = 1

	for i := 0; i < THREADS; i++ {
		go getRepoDataFromGithub(client, rateLimit, &wg, markdownRepoChan, githubRepoChan)
		wg.Add(1)
	}

	for i := 0; i < THREADS; i++ {
		go getContributorsFromGithub(client, rateLimit, &wg, githubRepoChan, githubRepoWithContributors)
		wg.Add(1)
	}

	counter := 0
	for len(githubRepoWithContributors) != len(markdownRepos) {
		fmt.Println("Waiting for all go routines to finish", len(githubRepoWithContributors), len(markdownRepos))
		time.Sleep(time.Second)
		counter++
		// fetch rate limit every 5 seconds
		if counter%5 == 0 {
			fmt.Println("Fetching rate limit")
			limits, _, err := client.RateLimits(context.Background())
			if err != nil {
				panic(err)
			}
			fmt.Println("Current Rate limit", limits.Core.Remaining)
			rateLimit.Store(int32(limits.Core.Remaining))
		}
	}

	fmt.Println("Waiting for all go routines to finish", len(githubRepoWithContributors), len(markdownRepos))

	close(markdownRepoChan)
	close(githubRepoChan)
	close(githubRepoWithContributors)
}

func getClient(authToken string) *github.Client {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: authToken},
	)
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}

func getRepoDataFromGithub(client *github.Client, rateLimit *atomic.Int32, wg *sync.WaitGroup, markdownRepoChan chan MarkdownRepo, githubRepoChan chan GithubRepo) {
	for markdownRepo := range markdownRepoChan {
		for rateLimit.Load() < 1 {
			markdownRepoChan <- markdownRepo
			fmt.Println("RepoContributors: waiting for rate limit to reset")
			time.Sleep(1 * time.Second)
			continue
		}

		rateLimit.Add(-1)

		if markdownRepo.GithubPagesName != "" {
			fmt.Println("skipping", markdownRepo.GithubPagesName, "until a lookup solution is found")
			continue
		}

		fmt.Println("getting REPO", markdownRepo.OwnerName, markdownRepo.RepoName)
		repo, resp, err := client.Repositories.Get(context.Background(), markdownRepo.OwnerName, markdownRepo.RepoName)
		if err != nil {
			if _, ok := err.(*github.RateLimitError); ok {
				markdownRepoChan <- markdownRepo
				continue
			}
			fmt.Println("error", err)
			githubRepoChan <- GithubRepo{
				MarkdownRepo: markdownRepo,
				Error:        err,
			}
			wg.Done()
			return
		}

		rateLimit.Store(int32(resp.Rate.Remaining))

		githubRepo := GithubRepo{
			MarkdownRepo: markdownRepo,
			Stars:        repo.GetStargazersCount(),
			LastCommit:   repo.GetUpdatedAt().Time,
		}

		githubRepoChan <- githubRepo
	}

	wg.Done()
}

func getContributorsFromGithub(client *github.Client, rateLimit *atomic.Int32, wg *sync.WaitGroup, githubRepoChan chan GithubRepo, githubRepoWithContributorsChan chan GithubRepo) {
	for githubRepo := range githubRepoChan {
		for rateLimit.Load() < 1 {
			fmt.Println("RepoContributors: waiting for rate limit to reset")
			githubRepoChan <- githubRepo
			time.Sleep(1 * time.Second)
			continue
		}

		rateLimit.Add(-1)

		fmt.Println("getting CONTRIB contributers", githubRepo.OwnerName, githubRepo.RepoName)
		contributers, resp, err := client.Repositories.ListContributors(context.Background(), githubRepo.OwnerName, githubRepo.RepoName, nil)

		if err != nil {
			if _, ok := err.(*github.RateLimitError); ok {
				githubRepoChan <- githubRepo
				continue
			}
			githubRepo.Error = err
			githubRepoWithContributorsChan <- githubRepo
			wg.Done()
			return
		}

		rateLimit.Store(int32(resp.Rate.Remaining))

		contributerMap := map[string]int{}

		for _, contributer := range contributers {
			contributerMap[*contributer.Login] = *contributer.Contributions
		}

		githubRepo.ContributerCount = len(contributers)
		githubRepo.Contributers = contributerMap
		githubRepoWithContributorsChan <- githubRepo
	}

	wg.Done()
}

func getText(URL string) string {
	resp, err := http.Get(URL)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	return string(body)
}

func parseGithubsURLRegex(text string) [][][]byte {
	reRepo := regexp.MustCompile(`\[([a-zA-Z0-9-_\/ ]+)\]\((https:\/\/(?:([a-zA-Z0-9-._]+)\.)?(?:github\.io|github\.com\/([a-zA-Z0-9-._]+)\/([a-zA-Z0-9-._]+)))\)(?: - (.+))`)
	return reRepo.FindAllSubmatch([]byte(text), -1)
}

func main() {
	getRepos := flag.Bool("update", false, "fetch repos from github and save it as json")
	flag.Parse()

	if *getRepos {
		githubToken := os.Getenv("GITHUB_TOKEN")
		if githubToken == "" {
			panic(errors.New("GITHUB_TOKEN is not set"))
		}

		markdownRepos, err := parseMarkdownRepos(githubToken)
		if err != nil {
			panic(err)
		}

		client := getClient(githubToken)
		githubRepos := getGithubReposFromMarkdownRepos(client, markdownRepos[0:10])

		reposAsBytes, err := reposToJson(githubRepos)
		if err != nil {
			panic(err)
		}

		err = bytesToFile(reposAsBytes)
		if err != nil {
			panic(err)
		}
	}
}
