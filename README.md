# Awesome Go Table
This repo parses the [awesome-go](https://github.com/avelino/awesome-go) repo and generates a index.html with more useful repo data.

This repo fetches updated info once a week on sunday at 00:00 UTC.

Visit the github pages here:
https://codeliger.github.io/awesome-go-table


If you are unhappy with the amount of columns being returned you can save the index.html from the link above and add more columns to the javascript.
Or you can make a pull request to this repo and I will consider adding it to the index.

## Implementation Flow
1. Go fetches the awesome-go markdown file with all the listed repos
2. Go fetches all the metadata for each repo from github
3. Go injects a script tag with a array of json objects called repoData. (Because cors won't let javascript grab from the releases directly)
4. The page gets saved and served as a github pages page.
5. The grid.js library loads the hardcoded json data into the table.
