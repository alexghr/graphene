package graphene

import (
	"fmt"
	"net/url"
	"strings"
)

type PullRequestURL struct {
	Branch string
	Base   string
	URL    string
}

func PullRequestURLs(template, remoteURL string, state State, branches []string) []PullRequestURL {
	var urls []PullRequestURL
	for _, branch := range branches {
		base, ok := BaseBranch(state, branch)
		if !ok {
			continue
		}

		pullURL, ok := pullRequestURL(template, remoteURL, base, branch)
		if !ok {
			continue
		}
		urls = append(urls, PullRequestURL{
			Branch: branch,
			Base:   base,
			URL:    pullURL,
		})
	}
	return urls
}

func pullRequestURL(template, remoteURL, base, branch string) (string, bool) {
	if template != "" {
		return applyPullRequestTemplate(template, base, branch), true
	}

	repoURL, ok := githubRepoURL(remoteURL)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s/compare/%s...%s?expand=1", repoURL, githubRef(base), githubRef(branch)), true
}

func applyPullRequestTemplate(template, base, branch string) string {
	url := strings.ReplaceAll(template, "${baseBranch}", base)
	url = strings.ReplaceAll(url, "${targetBranch}", branch)
	return url
}

func githubRepoURL(remoteURL string) (string, bool) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", false
	}

	if parsed, err := url.Parse(remoteURL); err == nil && parsed.Host != "" {
		if parsed.Hostname() != "github.com" {
			return "", false
		}
		owner, repo, ok := githubOwnerRepo(parsed.Path)
		if !ok {
			return "", false
		}
		return "https://github.com/" + owner + "/" + repo, true
	}

	hostPath := strings.SplitN(remoteURL, ":", 2)
	if len(hostPath) != 2 {
		return "", false
	}
	host := hostPath[0]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if host != "github.com" {
		return "", false
	}
	owner, repo, ok := githubOwnerRepo(hostPath[1])
	if !ok {
		return "", false
	}
	return "https://github.com/" + owner + "/" + repo, true
}

func githubOwnerRepo(path string) (string, string, bool) {
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return "", "", false
	}
	return parts[0], repo, true
}

func githubRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
