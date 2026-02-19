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

	"github.com/joho/godotenv"
	"github.com/shurcooL/githubv4"
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
			Description:     string(match[7]),
			Category:        category,
			SubCategory:     subCategory,
			SpecialCategory: specialCategory,
		}

		if strings.Contains(string(match[2]), ".github.io") {
			item.OwnerName = string(match[3])
			repoName := strings.TrimPrefix(string(match[4]), "/")
			if repoName != "" {
				item.RepoName = repoName
			} else {
				item.RepoName = item.OwnerName + ".github.io"
			}
		} else if string(match[3]) != "" {
			item.GithubPagesName = string(match[3])
		} else {
			item.OwnerName = string(match[5])
			item.RepoName = string(match[6])
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

func parseMarkdownRepos() ([]MarkdownRepo, error) {
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

func getGithubReposFromMarkdownRepos(client *githubv4.Client, markdownRepos []MarkdownRepo) []GithubRepo {
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

func manageGoRoutines(client *githubv4.Client, githubRepoWithContributors chan GithubRepo, markdownRepos []MarkdownRepo) {
	markdownRepoChan := make(chan MarkdownRepo, len(markdownRepos))
	githubRepoChan := make(chan GithubRepo, len(markdownRepos))

	for _, item := range markdownRepos {
		select {
		case markdownRepoChan <- item:
		default:
			fmt.Println("markdownRepoChan is closed")
		}
	}

	fmt.Println("markdown repo length", len(markdownRepos))

	rateLimit := &atomic.Int32{}

	// Get initial rate limit using GraphQL
	var rateLimitQuery struct {
		RateLimit struct {
			Cost      int
			Remaining int
			ResetAt   githubv4.DateTime
		}
	}

	err := client.Query(context.Background(), &rateLimitQuery, nil)
	if err != nil {
		fmt.Println("Error getting initial rate limit:", err)
		rateLimit.Store(5000) // Default to a high value if we can't fetch
	} else {
		rateLimit.Store(int32(rateLimitQuery.RateLimit.Remaining))
	}
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
			fmt.Printf("waiting for all tasks to finish %d/%d\n", len(githubRepoWithContributors), len(markdownRepos))

			fmt.Println("fetching ratelimit")
			var rateLimitCheckQuery struct {
				RateLimit struct {
					Cost      int
					Remaining int
					ResetAt   githubv4.DateTime
				}
			}

			err := client.Query(context.Background(), &rateLimitCheckQuery, nil)
			if err != nil {
				fmt.Println("unknown error when retrieving ratelimits sleeping 5 second", err)
				time.Sleep(5 * time.Second)
				continue
			} else {
				fmt.Printf("rate limit: remaining: %d; resets in %f seconds\n",
					rateLimitCheckQuery.RateLimit.Remaining,
					time.Until(rateLimitCheckQuery.RateLimit.ResetAt.Time).Seconds())
				fmt.Println("storing real rate limit")
				rateLimit.Store(int32(rateLimitCheckQuery.RateLimit.Remaining))
			}

			if rateLimit.Load() < 1 {
				fmt.Println("rate limit reached, sleeping for 60 seconds")
				time.Sleep(60 * time.Second)
			}
		}

		time.Sleep(time.Second)
		counter++
	}
}

func getClient(authToken string) *githubv4.Client {
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: authToken},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	return githubv4.NewClient(httpClient)
}

func getRepoDataFromGithub(client *githubv4.Client, rateLimit *atomic.Int32, wg *sync.WaitGroup, markdownRepoChan chan MarkdownRepo, githubRepoChan chan GithubRepo) {
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

			// Create GraphQL query for repository data
			var query struct {
				Repository struct {
					Name           string
					Description    string
					StargazerCount int
					WatcherCount   int
					ForkCount      int
					CreatedAt      githubv4.DateTime
					UpdatedAt      githubv4.DateTime
					PushedAt       githubv4.DateTime
					OpenIssues     struct {
						TotalCount int
					}
					LicenseInfo struct {
						Name string
					}
					IsArchived bool
				} `graphql:"repository(owner:$owner, name:$name)"`
			}

			variables := map[string]interface{}{
				"owner": githubv4.String(markdownRepo.OwnerName),
				"name":  githubv4.String(markdownRepo.RepoName),
			}

			err := client.Query(context.Background(), &query, variables)
			if err != nil {
				fmt.Println("error when getting repo", err)
				select {
				case githubRepoChan <- GithubRepo{
					MarkdownRepo: markdownRepo,
					Error:        err,
				}:
				default:
					fmt.Println("githubRepoChan is closed")
				}
				continue
			}

			githubRepo := GithubRepo{
				MarkdownRepo: markdownRepo,
				Stars:        query.Repository.StargazerCount,
				LastCommit:   query.Repository.UpdatedAt.Time,
				Watchers:     query.Repository.WatcherCount,
				Forks:        query.Repository.ForkCount,
				CreatedAt:    query.Repository.CreatedAt.Time,
				PushedAt:     query.Repository.PushedAt.Time,
				OpenIssues:   query.Repository.OpenIssues.TotalCount,
				License:      query.Repository.LicenseInfo.Name,
				Archived:     query.Repository.IsArchived,
			}

			select {
			case githubRepoChan <- githubRepo:
			default:
				fmt.Println("githubRepoChan is closed")
			}

		default:
			close(markdownRepoChan)
			wg.Done()
			return
		}
	}
}

func getContributorsFromGithub(client *githubv4.Client, rateLimit *atomic.Int32, wg *sync.WaitGroup, githubRepoChan chan GithubRepo, githubRepoWithContributorsChan chan GithubRepo, markdownRepoChan chan MarkdownRepo) {
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
				select {
				case githubRepoWithContributorsChan <- githubRepo:
				default:
					fmt.Println("githubRepoWithContributorsChan is closed")
				}
				continue
			}

			rateLimit.Add(-1)

			// Create GraphQL query for contributors data
			var contribQuery struct {
				Repository struct {
					Contributors struct {
						Nodes []struct {
							Login         string
							Contributions struct {
								TotalCount int
							}
						}
						TotalCount int
					} `graphql:"contributors(first:100)"`
				} `graphql:"repository(owner:$owner, name:$name)"`
			}

			variables := map[string]interface{}{
				"owner": githubv4.String(githubRepo.OwnerName),
				"name":  githubv4.String(githubRepo.RepoName),
			}

			err := client.Query(context.Background(), &contribQuery, variables)
			if err != nil {
				fmt.Println("error when getting contributers", err)
				githubRepo.Error = err
				select {
				case githubRepoWithContributorsChan <- githubRepo:
				default:
					fmt.Println("githubRepoWithContributorsChan is closed")
				}
				continue
			}

			contributerMap := map[string]int{}

			for _, contributer := range contribQuery.Repository.Contributors.Nodes {
				contributerMap[contributer.Login] = contributer.Contributions.TotalCount
			}

			githubRepo.ContributerCount = contribQuery.Repository.Contributors.TotalCount
			githubRepo.Contributers = contributerMap
			select {
			case githubRepoWithContributorsChan <- githubRepo:
			default:
				fmt.Println("githubRepoWithContributorsChan is closed")
			}
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
	reRepo := regexp.MustCompile(`\[([a-zA-Z0-9-_\/ ]+)\]\((https:\/\/(?:([a-zA-Z0-9-._]+)\.)?(?:github\.io(\/[a-zA-Z0-9-._]*)?|github\.com\/([a-zA-Z0-9-._]+)\/([a-zA-Z0-9-._]+)))\)(?: - (.+))`)
	return reRepo.FindAllSubmatch([]byte(text), -1)
}

func addScriptToIndex(bytes []byte) {
	f, err := os.OpenFile("template.html", os.O_RDWR, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	f2, err := os.Create("index.html")
	if err != nil {
		panic(err)
	}
	defer f2.Close()

	doc, err := html.Parse(f)
	if err != nil {
		panic(err)
	}

	// Find the script element with id="data" and update its content
	var recurseHTML func(*html.Node)
	recurseHTML = func(n *html.Node) {
		// Check if this is the script element with id="data"
		if n.Type == html.ElementNode && n.Data == "script" {
			for _, attr := range n.Attr {
				if attr.Key == "id" && attr.Val == "data" {
					// Found the script element, update its content
					// Clear existing children
					n.FirstChild = nil
					n.LastChild = nil

					// Add the data as a text node
					n.AppendChild(&html.Node{
						Type: html.TextNode,
						Data: string(bytes),
					})
					return
				}
			}
		}

		// Recursively process child nodes
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

	err := godotenv.Load()
	fmt.Println(err)

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		panic(errors.New("GITHUB_TOKEN is not set"))
	}

	if *getRepos {
		markdownRepos, err := parseMarkdownRepos()
		if err != nil {
			panic(err)
		}

		client := getClient(githubToken)
		githubRepos := getGithubReposFromMarkdownRepos(client, markdownRepos)

		repoBytes, err := json.Marshal(githubRepos)
		if err != nil {
			panic(err)
		}

		err = bytesToFile(repoBytes)
		if err != nil {
			panic(err)
		}
	}

	jsonBytes, err := os.ReadFile("github_repos.json")
	if err != nil {
		fmt.Println("local github_repos.json not found")
	}

	// don't refetch the latest release remotely if the latest was fetched locally
	if !*getRepos && *latestRelease {
		jsonBytes, err = getReleaseJSON()
		if err != nil {
			panic(err)
		}
	}

	if *saveInHTML {
		if err != nil {
			return
		}
		githubRepos := []GithubRepo{}
		err = json.Unmarshal(jsonBytes, &githubRepos)
		if err != nil {
			panic(err)
		}

		remarshalledBytes, err := json.Marshal(githubRepos)
		if err != nil {
			panic(err)
		}

		addScriptToIndex(remarshalledBytes)
	}

	if *testRateLimit {
		client := getClient(githubToken)
		var rateLimitQuery struct {
			RateLimit struct {
				Cost      int
				Remaining int
				ResetAt   githubv4.DateTime
			}
		}

		err := client.Query(context.Background(), &rateLimitQuery, nil)
		if err != nil {
			fmt.Println(err)
		}

		fmt.Printf("Rate limit: cost=%d, remaining=%d, reset_at=%s\n",
			rateLimitQuery.RateLimit.Cost,
			rateLimitQuery.RateLimit.Remaining,
			rateLimitQuery.RateLimit.ResetAt)
		fmt.Println(err)
	}
}
