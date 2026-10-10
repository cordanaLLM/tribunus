package releasewatch

import (
	"errors"
	"time"
)

const (
	DefaultGitHubAPIBase      = "https://api.github.com"
	DefaultHuggingFaceAPIBase = "https://huggingface.co"
	DefaultNtfyBase           = "https://ntfy.sh"
	MaxSeenRecords            = 10000
	MaxNotesRunes             = 2000
	MaxSearchIssuePages       = 5
	SeenFileName              = "release-watch-seen.json"
)

var (
	ErrThrottled    = errors.New("release-watch: rate limit throttled")
	ErrMissingToken = errors.New("release-watch: GITHUB_TOKEN required to file issue")
)

type ReleaseItem struct {
	ID          string
	SourceType  string
	SourceName  string
	TagOrModel  string
	URL         string
	Notes       string
	PublishedAt time.Time
}

type GitHubReleasePayload struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
}

type GitHubTagPayload struct {
	Name string `json:"name"`
}

type HuggingFaceModelPayload struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

type GitHubIssuePayload struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

type GitHubIssueResponse struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
}
