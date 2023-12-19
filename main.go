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
	"github.com/joho/godotenv"
	"golang.org/x/net/html"
	"golang.org/x/oauth2"
)

const (
	SPECIALCATEGORY = "#### "
	SUBCATEGORY     = "### "
	CATEGORY        = "## "
)

type MarkdownRepo struct {
	Category        string `json:"category"`
	SubCategory     string `json:"subcategory"`
	SpecialCategory string `json:"special_category"`
	ProjectName     string `json:"project_name"`
	Description     string `json:"description"`
	OwnerName       string `json:"owner_name"`
	RepoName        string `json:"repo_name"`
	GithubPagesName string `json:"github_pages_name"`
	URL             string `json:"url"`
}

type GithubRepo struct {
	MarkdownRepo
	Stars            int            `json:"stars"`
	Watchers         int            `json:"watchers"`
	CreatedAt        time.Time      `json:"created_at"`
	PushedAt         time.Time      `json:"pushed_at"`
	LastCommit       time.Time      `json:"last_commit"`
	Forks            int            `json:"forks"`
	OpenIssues       int            `json:"open_issues"`
	ContributerCount int            `json:"contributer_count"`
	License          string         `json:"license"`
	Archived         bool           `json:"archived"`
	Contributers     map[string]int `json:"contributers"`
	Error            error          `json:"error,omitempty"`
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
			SubCategory:     subCategory,
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

func bytesToFile(bytes []byte) error {
	fileName := getFileName()
	err := os.WriteFile(fileName, bytes, 0o644)
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

	filteredRepos := []MarkdownRepo{}

	for _, repo := range markdownRepos {
		if repo.GithubPagesName == "" {
			filteredRepos = append(filteredRepos, repo)
		} else {
			fmt.Printf("skipping custom domain repo %+v\n", repo)
		}
	}

	return filteredRepos, nil
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

	fmt.Println("markdown repo length", len(markdownRepos))

	rateLimit := &atomic.Int32{}

	limits, _, err := client.RateLimits(context.Background())
	if err != nil {
		fmt.Println(err)
	}

	rateLimit.Store(int32(limits.Core.Remaining))
	fmt.Println("initial rate limit", rateLimit.Load())

	wg := sync.WaitGroup{}

	const THREADS = 1

	for i := 0; i < THREADS; i++ {
		go getRepoDataFromGithub(client, rateLimit, &wg, markdownRepoChan, githubRepoChan)
		wg.Add(1)
	}

	for i := 0; i < THREADS; i++ {
		go getContributorsFromGithub(client, rateLimit, &wg, githubRepoChan, githubRepoWithContributors, markdownRepoChan)
		wg.Add(1)
	}

	counter := 0
	fmt.Println("initial sleep time", 30)
	time.Sleep(time.Second * 30)

	for len(githubRepoChan) > 0 || len(markdownRepoChan) > 0 {
		// fetch rate limit every 5 seconds
		if counter%10 == 0 && counter != 0 {
			fmt.Printf("waiting for all go routines to finish %d/%d [limit:%d]\n", len(githubRepoWithContributors), len(markdownRepos), rateLimit.Load())

			fmt.Println("fetching ratelimit")
			limits, _, err := client.RateLimits(context.Background())
			fmt.Println(limits)
			if err != nil {
				if rateLimitErr, ok := err.(*github.RateLimitError); ok {
					fmt.Println("rate limit reached while fetching ratelimits sleeping until reset time", time.Until(rateLimitErr.Rate.Reset.Time).Seconds())
					time.Sleep(time.Until(rateLimitErr.Rate.Reset.Time))
				} else {
					fmt.Println("unknown error when retrieving ratelimits sleeping 5 second", err)
					time.Sleep(5 * time.Second)
				}
				continue
			} else {
				fmt.Printf("expected rate limit %d; actual rate limit %d; updating\n", rateLimit.Load(), limits.Core.Remaining)
				rateLimit.Store(int32(limits.Core.Remaining))
			}

			if rateLimit.Load() < 1 {
				fmt.Println("rate limit reached, sleeping for", time.Until(limits.Core.Reset.Time).Seconds())
				time.Sleep(time.Until(limits.Core.Reset.Time) + (time.Second * 5))
			}
		}

		time.Sleep(time.Second)
		counter++
	}
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
	for {
		for rateLimit.Load()-1 < 1 {
			fmt.Println("GithubRepo: waiting for rate limit to reset", rateLimit.Load())
			time.Sleep(1 * time.Second)
			continue
		}

		select {
		case markdownRepo, ok := <-markdownRepoChan:
			if !ok {
				fmt.Println("GithubRepo: channel closed")
				wg.Done()
				return
			}

			rateLimit.Add(-1)

			repo, _, err := client.Repositories.Get(context.Background(), markdownRepo.OwnerName, markdownRepo.RepoName)
			if err != nil {
				if strings.Contains(err.Error(), "secondary") {
					fmt.Println("secondary rate limit reached, sleeping for 3 seconds")
					time.Sleep(3 * time.Second)

					markdownRepoChan <- markdownRepo
					continue
				}
				if rateLimitErr, ok := err.(*github.RateLimitError); ok {
					fmt.Println("repo function returned ratelimit error")
					rateLimit.Store(int32(rateLimitErr.Rate.Remaining))

					markdownRepoChan <- markdownRepo
					continue
				}

				fmt.Println("error when getting repo", err)
				githubRepoChan <- GithubRepo{
					MarkdownRepo: markdownRepo,
					Error:        err,
				}
				continue
			}

			githubRepo := GithubRepo{
				MarkdownRepo: markdownRepo,
				Stars:        repo.GetStargazersCount(),
				LastCommit:   repo.GetUpdatedAt().Time,
				Watchers:     repo.GetWatchersCount(),
				Forks:        repo.GetForksCount(),
				CreatedAt:    repo.GetCreatedAt().Time,
				PushedAt:     repo.GetPushedAt().Time,
				OpenIssues:   repo.GetOpenIssuesCount(),
				License:      repo.GetLicense().GetName(),
				Archived:     repo.GetArchived(),
			}

			githubRepoChan <- githubRepo

		default:
			close(markdownRepoChan)
			wg.Done()
			return
		}
	}
}

func getContributorsFromGithub(client *github.Client, rateLimit *atomic.Int32, wg *sync.WaitGroup, githubRepoChan chan GithubRepo, githubRepoWithContributorsChan chan GithubRepo, markdownRepoChan chan MarkdownRepo) {
	for {
		for rateLimit.Load()-1 < 1 {
			fmt.Println("RepoContributors: waiting for rate limit to reset", rateLimit.Load())
			time.Sleep(1 * time.Second)
			continue
		}

		select {
		case githubRepo, ok := <-githubRepoChan:
			// hypothetical for multiple threads (secondary rate limit is reached using more than 1 thread)
			if !ok {
				fmt.Println("RepoContributors: channel closed")
				wg.Done()
				return
			}

			if githubRepo.Error != nil {
				githubRepoWithContributorsChan <- githubRepo
				continue
			}

			rateLimit.Add(-1)

			contributers, _, err := client.Repositories.ListContributors(context.Background(), githubRepo.OwnerName, githubRepo.RepoName, nil)
			if err != nil {
				if rateLimitErr, ok := err.(*github.RateLimitError); ok {
					fmt.Println("contrib function returned ratelimit error")
					rateLimit.Store(int32(rateLimitErr.Rate.Remaining))
					if strings.Contains(rateLimitErr.Message, "secondary") {
						fmt.Println("secondary rate limit reached, sleeping for 3 seconds")
						time.Sleep(3 * time.Second)
					}
					githubRepoChan <- githubRepo
					continue
				}

				fmt.Println("error when getting contributers", err)
				githubRepo.Error = err
				githubRepoWithContributorsChan <- githubRepo
				continue
			}

			contributerMap := map[string]int{}

			for _, contributer := range contributers {
				contributerMap[*contributer.Login] = *contributer.Contributions
			}

			githubRepo.ContributerCount = len(contributers)
			githubRepo.Contributers = contributerMap
			githubRepoWithContributorsChan <- githubRepo
		default:
			if len(markdownRepoChan) == 0 {
				close(githubRepoChan)
				close(githubRepoWithContributorsChan)
				wg.Done()
				return
			}
		}
	}
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

func addJSONToIndex(bytes []byte) {
	f, err := os.OpenFile("template.html", os.O_RDWR, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	f2, err := os.Create("index.html")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	doc, err := html.Parse(f)
	if err != nil {
		panic(err)
	}

	var recurseHTML func(*html.Node)
	recurseHTML = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "head" {
			script := &html.Node{
				Type: html.ElementNode,
				Data: "script",
				Attr: []html.Attribute{
					{
						Key: "id",
						Val: "data",
					},
				},
			}
			script.AppendChild(&html.Node{
				Type: html.TextNode,
				Data: "const repoData = " + string(bytes),
			})
			n.AppendChild(script)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			recurseHTML(c)
		}
	}
	recurseHTML(doc)

	err = html.Render(f2, doc)
	if err != nil {
		panic(err)
	}
}

func getReleaseJSON() ([]byte, error) {
	resp, err := http.Get("https://api.github.com/repos/codeliger/awesome-go-table/releases/latest")
	if err != nil {
		return []byte{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, err
	}

	type Asset struct {
		BrowserDownloadURL string `json:"browser_download_url"`
	}

	type Release struct {
		Assets []Asset
	}

	release := Release{}
	err = json.Unmarshal(body, &release)
	if err != nil {
		return []byte{}, err
	}

	resp, err = http.Get(release.Assets[0].BrowserDownloadURL)
	if err != nil {
		return []byte{}, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, err
	}

	return body, err
}

func main() {
	getRepos := flag.Bool("update", false, "fetch repos from github and save it as json")
	testRateLimit := flag.Bool("test", false, "test rate limit")
	latestRelease := flag.Bool("latest", false, "fetch latest build artifact")
	saveInHTML := flag.Bool("save", false, "save in html")

	flag.Parse()

	godotenv.Load()

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		panic(errors.New("GITHUB_TOKEN is not set"))
	}

	if *latestRelease {
		jsonBytes, err := getReleaseJSON()
		if err != nil {
			panic(err)
		}

		if *saveInHTML {
			githubRepos := []GithubRepo{}
			err = json.Unmarshal(jsonBytes, &githubRepos)
			if err != nil {
				panic(err)
			}

			remarshalledBytes, err := json.Marshal(githubRepos)
			if err != nil {
				panic(err)
			}
			addJSONToIndex(remarshalledBytes)
		}
	} else if *getRepos {
		markdownRepos, err := parseMarkdownRepos(githubToken)
		if err != nil {
			panic(err)
		}

		client := getClient(githubToken)
		githubRepos := getGithubReposFromMarkdownRepos(client, markdownRepos)

		bytes, err := json.Marshal(githubRepos)
		if err != nil {
			panic(err)
		}

		err = bytesToFile(bytes)
		if err != nil {
			panic(err)
		}

		if *saveInHTML {
			addJSONToIndex(bytes)
		}
	} else if *saveInHTML {
		jsonBytes, err := os.ReadFile("github_repos.json")
		if err != nil {
			panic(err)
		}

		// WHY DO I NEED TO RETRANSFORM THE JSON SO IT WORKS IN JAVASCRIPT?
		githubRepos := []GithubRepo{}
		err = json.Unmarshal(jsonBytes, &githubRepos)
		if err != nil {
			panic(err)
		}

		remarshalledBytes, err := json.Marshal(githubRepos)
		if err != nil {
			panic(err)
		}

		addJSONToIndex(remarshalledBytes)
	}

	if *testRateLimit {
		client := getClient(githubToken)
		limits, resp, err := client.RateLimits(context.Background())
		if err != nil {
			fmt.Println(err)
		}

		fmt.Println(limits)
		fmt.Println(resp)
		fmt.Println(err)
	}
}
