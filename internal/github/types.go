package github

import "time"

// Release is the subset of a GitHub release object the resolver needs: the
// tag it points at, its channel (draft / pre-release / stable), when it was
// published and the assets attached to it.
type Release struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Asset is one downloadable file attached to a Release.
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	ContentType        string `json:"content_type"`
}

// Tag is one entry of a repository's tag list.
type Tag struct {
	Name   string    `json:"name"`
	Commit TagCommit `json:"commit"`
}

// TagCommit carries the commit a Tag points at.
type TagCommit struct {
	SHA string `json:"sha"`
}

// Repository is the subset of the repository object the resolver needs: the
// default branch name and the time of the last push.
type Repository struct {
	DefaultBranch string    `json:"default_branch"`
	PushedAt      time.Time `json:"pushed_at"`
}

// Commit is the subset of a commit object: its SHA and the nested commit
// metadata that carries the committer date.
type Commit struct {
	SHA    string     `json:"sha"`
	Commit CommitData `json:"commit"`
}

// CommitData is the nested "commit" object of a Commit.
type CommitData struct {
	Committer CommitAuthor `json:"committer"`
}

// CommitAuthor is the identity and date of a commit's author or committer.
type CommitAuthor struct {
	Date time.Time `json:"date"`
}

// CompareResult is the subset of a compare response the staleness detector
// needs: the list of files the base...head delta touched.
type CompareResult struct {
	TotalCommits int           `json:"total_commits"`
	Files        []CompareFile `json:"files"`
}

// CompareFile is one changed file in a compare result.
type CompareFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
}
