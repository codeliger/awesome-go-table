package main

import (
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
	"time"

	graphql "github.com/hasura/go-graphql-client"
	"github.com/joho/godotenv"
	"golang.org/x/net/html"
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
	Stars      int       `json:"stars"`
	Watchers   int       `json:"watchers"`
	CreatedAt  time.Time `json:"created_at"`
	PushedAt   time.Time `json:"pushed_at"`
	LastCommit time.Time `json:"last_commit"`
	Forks      int       `json:"forks"`
	OpenIssues int       `json:"open_issues"`
	License    string    `json:"license"`
	Archived   bool      `json:"archived"`
	Error      error     `json:"error,omitempty"`
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

func bytesToFile(bytes []byte) error {
	fileName := getFileName()
	err := os.WriteFile(fileName, bytes, 0o644)
	if err != nil {
		return err
	}

	return nil
}

func parseMarkdownRepos() ([]MarkdownRepo, error) {
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

func getGithubReposFromMarkdownRepos(gqlClient *graphql.Client, markdownRepos []MarkdownRepo) []GithubRepo {
	githubRepoChan := make(chan GithubRepo, len(markdownRepos))

	manageGoRoutines(gqlClient, githubRepoChan, markdownRepos)

	githubRepos := make([]GithubRepo, 0, len(githubRepoChan))

	for githubRepo := range githubRepoChan {
		if githubRepo.Error != nil {
			fmt.Println("Error", githubRepo.Error)
		} else {
			githubRepos = append(githubRepos, githubRepo)
		}
	}

	return githubRepos
}

func manageGoRoutines(gqlClient *graphql.Client, githubRepoChan chan GithubRepo, markdownRepos []MarkdownRepo) {
	const BATCH_SIZE = 20
	markdownRepoBatchChan := make(chan []MarkdownRepo, (len(markdownRepos)+BATCH_SIZE-1)/BATCH_SIZE)

	for i := 0; i < len(markdownRepos); i += BATCH_SIZE {
		end := min(i+BATCH_SIZE, len(markdownRepos))
		markdownRepoBatchChan <- markdownRepos[i:end]
	}
	close(markdownRepoBatchChan)

	// Get initial rate limit using GraphQL
	rateLimitInfo, err := GetRateLimit(gqlClient)
	if err != nil {
		panic(err)
	}

	fmt.Println("initial rate limit", rateLimitInfo.Remaining)

	wg := sync.WaitGroup{}

	const THREADS = 3

	for range THREADS {
		go getRepoDataBatchFromGithub(gqlClient, &wg, markdownRepoBatchChan, githubRepoChan)
		wg.Add(1)
	}

	wg.Wait()
	close(githubRepoChan)
}

func getRepoDataBatchFromGithub(gqlClient *graphql.Client, wg *sync.WaitGroup, markdownRepoBatchChan chan []MarkdownRepo, githubRepoChan chan GithubRepo) {
	for batch := range markdownRepoBatchChan {
		fmt.Printf("fetching batch of %d repos\n", len(batch))

		var innerWg sync.WaitGroup
		for _, repo := range batch {
			innerWg.Add(1)
			go func(r MarkdownRepo) {
				defer innerWg.Done()

				ghRepo, err := GetRepositoryData(gqlClient, r.OwnerName, r.RepoName)
				if err != nil {
					githubRepoChan <- GithubRepo{
						MarkdownRepo: r,
						Error:        err,
					}
					return
				}

				if ghRepo == nil {
					githubRepoChan <- GithubRepo{
						MarkdownRepo: r,
						Error:        fmt.Errorf("repository %s/%s not found", r.OwnerName, r.RepoName),
					}
					return
				}

				createdAt, _ := time.Parse(time.RFC3339, ghRepo.CreatedAt)
				updatedAt, _ := time.Parse(time.RFC3339, ghRepo.UpdatedAt)
				pushedAt, _ := time.Parse(time.RFC3339, ghRepo.PushedAt)

				license := ""
				if ghRepo.LicenseInfo != nil {
					license = ghRepo.LicenseInfo.Name
				}

				githubRepoChan <- GithubRepo{
					MarkdownRepo: r,
					Stars:        ghRepo.StargazerCount,
					Watchers:     ghRepo.Watchers.TotalCount,
					Forks:        ghRepo.ForkCount,
					OpenIssues:   ghRepo.Issues.TotalCount,
					CreatedAt:    createdAt,
					LastCommit:   updatedAt,
					PushedAt:     pushedAt,
					Archived:     ghRepo.IsArchived,
					License:      license,
				}
			}(repo)
		}

		innerWg.Wait()
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
	updateRepos := flag.Bool("update", false, "fetch repos from github and save it as json")
	testRateLimit := flag.Bool("test", false, "test rate limit")
	latestRelease := flag.Bool("latest", false, "fetch latest build artifact")
	saveInHTML := flag.Bool("save", false, "save in html")
	requestLimit := flag.Int("limit", 0, "limit the number of requests made (0 for no limit)")

	if len(os.Args) == 1 {
		flag.Usage()
		return
	}

	flag.Parse()

	err := godotenv.Load()
	fmt.Println(err)

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		panic(errors.New("GITHUB_TOKEN is not set"))
	}

	if *updateRepos {
		markdownRepos, err := parseMarkdownRepos()
		if err != nil {
			panic(err)
		}
		fmt.Println("processed markdown repos", len(markdownRepos))

		gqlClient := NewGitHubGraphQLClient(githubToken)

		// Apply request limit if specified
		if *requestLimit > 0 && len(markdownRepos) > *requestLimit {
			markdownRepos = markdownRepos[:*requestLimit]
			fmt.Printf("Limiting requests to %d repositories\n", *requestLimit)
		}

		githubRepos := getGithubReposFromMarkdownRepos(gqlClient, markdownRepos)

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
	if !*updateRepos && *latestRelease {
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
		gqlClient := NewGitHubGraphQLClient(githubToken)
		rateLimit, err := GetRateLimit(gqlClient)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Printf("Rate limit: cost=%d, remaining=%d, reset_at=%s\n",
			rateLimit.Cost,
			rateLimit.Remaining,
			rateLimit.ResetAt)
	}
}
