package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type RepoStatus struct {
	HasChanges    bool     `json:"hasChanges"`
	ChangedFiles  []string `json:"changedFiles"`
	CurrentBranch string   `json:"currentBranch"`
	IsClean       bool     `json:"isClean"`
}

type OllamaRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type OllamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

type GitHubPRRequest struct {
	Title               string `json:"title"`
	Head                string `json:"head"`
	Base                string `json:"base"`
	Body                string `json:"body"`
	Draft               bool   `json:"draft"`
	MaintainerCanModify bool   `json:"maintainer_can_modify"`
}

type GitHubPRResponse struct {
	Number int `json:"number"`
}

type BranchChanges struct {
	Files   []string
	Commits []*object.Commit
	Summary string
}

type Changes struct {
	Files   []string
	Commits []string
	Summary string
}

type AIService struct {
	Server string
	Model  string
	Type   string
	APIKey string
}

func GetRepoStatus(path string) (*RepoStatus, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, err
	}

	w, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	status, err := w.Status()
	if err != nil {
		return nil, err
	}

	head, err := repo.Head()
	if err != nil {
		return nil, err
	}

	changedFiles := []string{}
	for file, fileStatus := range status {
		if fileStatus.Staging != git.Unmodified || fileStatus.Worktree != git.Unmodified {
			changedFiles = append(changedFiles, file)
		}
	}

	return &RepoStatus{
		HasChanges:    !status.IsClean(),
		ChangedFiles:  changedFiles,
		CurrentBranch: head.Name().Short(),
		IsClean:       status.IsClean(),
	}, nil
}

func CommitChanges(path string, aiService AIService) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}

	w, err := repo.Worktree()
	if err != nil {
		return err
	}

	status, err := w.Status()
	if err != nil {
		return err
	}

	if status.IsClean() {
		return nil
	}

	// Add all changes
	_, err = w.Add(".")
	if err != nil {
		return err
	}

	changes, err := getChanges(repo)
	if err != nil {
		return err
	}

	message, err := generateCommitMessage(changes, aiService)
	if err != nil {
		return err
	}

	_, err = w.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "GitWatcher",
			Email: "gitwatcher@local",
			When:  time.Now(),
		},
	})

	return err
}

func getSSHAuth() (*ssh.PublicKeys, error) {
	sshPath := os.Getenv("SSH_KEY_PATH")
	if sshPath == "" {
		// Default to standard SSH key location
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		sshPath = filepath.Join(homeDir, ".ssh", "id_rsa")
	}

	publicKeys, err := ssh.NewPublicKeysFromFile("git", sshPath, "")
	if err != nil {
		return nil, fmt.Errorf("error loading SSH key: %v", err)
	}
	return publicKeys, nil
}

func PushChanges(path string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}

	// Get SSH authentication
	auth, err := getSSHAuth()
	if err != nil {
		return fmt.Errorf("SSH authentication error: %v", err)
	}

	currentBranch, err := repo.Head()
	if err != nil {
		return err
	}

	refSpecStr := fmt.Sprintf(
		"+%s:refs/heads/%s",
		currentBranch.Name().String(),
		currentBranch.Name().Short(),
	)
	refSpec := config.RefSpec(refSpecStr)
	log.Printf("Pushing %s", refSpec)
	// Update push options to include SSH auth
	return repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refSpec},
		Auth:       auth,
	})
}

func generateCommitMessage(changes *Changes, aiService AIService) (string, error) {
	if aiService.Type == "gemini" {
		return generateGeminiCommitMessage(changes, aiService)
	}
	return generateOllamaCommitMessage(changes, aiService)
}

func CreateBranch(path string, branchName string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}

	head, err := repo.Head()
	if err != nil {
		return err
	}

	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), head.Hash())
	return repo.Storer.SetReference(ref)
}

func CheckoutBranch(path string, branchName string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}

	w, err := repo.Worktree()
	if err != nil {
		return err
	}

	return w.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branchName),
	})
}

func FetchRepository(path string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}

	auth, err := getSSHAuth()
	if err != nil {
		return fmt.Errorf("SSH authentication error: %v", err)
	}

	err = repo.Fetch(&git.FetchOptions{
		Auth: auth,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return err
	}
	return nil
}

func getBranchChanges(repo *git.Repository, currentBranch string, targetBranch string) (*BranchChanges, error) {
	// Get references
	currentRef, err := repo.Reference(plumbing.NewBranchReferenceName(currentBranch), true)
	if err != nil {
		return nil, fmt.Errorf("error getting current branch ref: %v", err)
	}

	targetRef, err := repo.Reference(plumbing.NewBranchReferenceName(targetBranch), true)
	if err != nil {
		return nil, fmt.Errorf("error getting target branch ref: %v", err)
	}

	// Get commit objects
	currentCommit, err := repo.CommitObject(currentRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("error getting current commit: %v", err)
	}

	targetCommit, err := repo.CommitObject(targetRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("error getting target commit: %v", err)
	}

	// Find common ancestor
	isAncestor := false
	var mergeBase *object.Commit

	// First check if target is ancestor of current
	isAncestor, err = currentCommit.IsAncestor(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("error checking ancestry: %v", err)
	}

	if isAncestor {
		mergeBase = targetCommit
	} else {
		// Then check if current is ancestor of target
		isAncestor, err = targetCommit.IsAncestor(currentCommit)
		if err != nil {
			return nil, fmt.Errorf("error checking ancestry: %v", err)
		}
		if isAncestor {
			mergeBase = currentCommit
		} else {
			// Find the most recent common ancestor
			commits, err := currentCommit.MergeBase(targetCommit)
			if err != nil {
				return nil, fmt.Errorf("error finding merge base: %v", err)
			}
			if len(commits) == 0 {
				return nil, fmt.Errorf("no common ancestor found between branches")
			}
			mergeBase = commits[0]
		}
	}

	// Get commit history from current branch up to merge base
	cIter, err := repo.Log(&git.LogOptions{From: currentRef.Hash()})
	if err != nil {
		return nil, fmt.Errorf("error getting commit history: %v", err)
	}

	var commits []*object.Commit
	var files = make(map[string]struct{})
	var summary strings.Builder

	err = cIter.ForEach(func(c *object.Commit) error {
		// Stop when we reach the merge base
		if c.Hash == mergeBase.Hash {
			return io.EOF
		}

		commits = append(commits, c)
		summary.WriteString("- " + c.Message + "\n")

		// Get files changed in this commit
		stats, err := c.Stats()
		if err != nil {
			return err
		}

		for _, stat := range stats {
			files[stat.Name] = struct{}{}
		}

		return nil
	})

	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("error iterating commits: %v", err)
	}

	// Convert files map to slice
	var filesList []string
	for file := range files {
		filesList = append(filesList, file)
	}

	return &BranchChanges{
		Files:   filesList,
		Commits: commits,
		Summary: summary.String(),
	}, nil
}

func generateGeminiCommitMessage(changes *Changes, aiService AIService) (string, error) {
	prompt := fmt.Sprintf("Generate a concise commit message for the following changes\n"+
		"no placeholders, explanation, or other text should be provided\n"+
		"limit the message to 72 characters\n\n%s", formatChangesForPrompt(changes))

	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(aiService.APIKey))
	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %v", err)
	}
	defer client.Close()

	geminiModel := client.GenerativeModel(aiService.Model)

	resp, err := geminiModel.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %v", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response from Gemini API")
	}

	text, ok := resp.Candidates[0].Content.Parts[0].(genai.Text)
	if !ok {
		return "", fmt.Errorf("unexpected response type from Gemini API")
	}

	return string(text), nil
}

func generateOllamaCommitMessage(changes *Changes, aiService AIService) (string, error) {
	prompt := fmt.Sprintf("Generate a concise commit message for the following changes\n"+
		"no placeholders, explanation, or other text should be provided\n"+
		"limit the message to 72 characters\n\n%s", formatChangesForPrompt(changes))

	req := OllamaRequest{
		Model: aiService.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(aiService.Server+"/api/chat", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama API error: %s", string(body))
	}

	var response OllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", err
	}

	return response.Message.Content, nil
}

func generateGeminiPRDescription(changes *Changes, aiService AIService) (string, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(aiService.APIKey))
	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %v", err)
	}
	defer client.Close()

	geminiModel := client.GenerativeModel(aiService.Model)

	prompt := fmt.Sprintf("Generate a detailed pull request description for the following changes:\n\nCommits:\n%s\n\nChanged files:\n%v\n\n"+
		"The description should include:\n"+
		"1. A summary of the changes\n"+
		"2. The motivation for the changes\n"+
		"3. Any potential impact or breaking changes\n"+
		"4. Testing instructions if applicable\n\n"+
		"Format the response in markdown.\n"+
		"Do not include any other text in the response.\n"+
		"Do not include any placeholders in the response. It is expected to be a complete description.\n"+
		"Provide the output as markdown, but do not wrap it in a code block.\n\n",
		changes.Summary, changes.Files)

	resp, err := geminiModel.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %v", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response from Gemini API")
	}

	text, ok := resp.Candidates[0].Content.Parts[0].(genai.Text)
	if !ok {
		return "", fmt.Errorf("unexpected response type from Gemini API")
	}

	return string(text), nil
}

func generateOllamaPRDescription(changes *Changes, aiService AIService) (string, error) {
	prompt := fmt.Sprintf("Generate a detailed pull request description for the following changes:\n\nCommits:\n%s\n\nChanged files:\n%v\n\n"+
		"The description should include:\n"+
		"1. A summary of the changes\n"+
		"2. The motivation for the changes\n"+
		"3. Any potential impact or breaking changes\n"+
		"4. Testing instructions if applicable\n\n"+
		"Format the response in markdown.\n"+
		"Do not include any other text in the response.\n"+
		"Do not include any placeholders in the response. It is expected to be a complete description.",
		changes.Summary, changes.Files)

	req := OllamaRequest{
		Model: aiService.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(aiService.Server+"/api/chat", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama API error: %s", string(body))
	}

	var response OllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", err
	}

	return response.Message.Content, nil
}

func generatePRTitle(changes *Changes, aiService AIService) (string, error) {
	if aiService.Type == "gemini" {
		return generateGeminiCommitMessage(changes, aiService)
	}
	return generateOllamaCommitMessage(changes, aiService)
}

func generatePRDescription(changes *Changes, aiService AIService) (string, error) {
	if aiService.Type == "gemini" {
		return generateGeminiPRDescription(changes, aiService)
	}
	return generateOllamaPRDescription(changes, aiService)
}

func CreateDraftPR(path string, aiService AIService, githubToken string) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}

	// Get current branch name
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("error getting HEAD: %v", err)
	}
	currentBranch := strings.TrimPrefix(string(head.Name()), "refs/heads/")

	// Get remote URL to extract owner and repo name
	remote, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("error getting remote: %v", err)
	}

	remoteURL := remote.Config().URLs[0]
	// Extract owner and repo from SSH URL format (git@github.com:owner/repo.git)
	// or HTTPS URL format (https://github.com/owner/repo.git)
	var owner, repoName string
	if strings.Contains(remoteURL, "git@github.com:") {
		parts := strings.Split(strings.TrimPrefix(remoteURL, "git@github.com:"), "/")
		owner = parts[0]
		repoName = strings.TrimSuffix(parts[1], ".git")
	} else {
		parts := strings.Split(strings.TrimPrefix(remoteURL, "https://github.com/"), "/")
		owner = parts[0]
		repoName = strings.TrimSuffix(parts[1], ".git")
	}

	// Get changes for PR content
	changes, err := getChanges(repo)
	if err != nil {
		return fmt.Errorf("error getting changes: %v", err)
	}

	log.Println("Starting PR generation")

	// Generate PR title and description
	prTitle, err := generatePRTitle(changes, aiService)
	if err != nil {
		return err
	}

	prDescription, err := generatePRDescription(changes, aiService)
	if err != nil {
		return err
	}

	log.Printf("PR title: %s\nPR description: %s\n", prTitle, prDescription)
	log.Println("PR generation complete")

	// Create PR request
	prRequest := GitHubPRRequest{
		Title:               prTitle,
		Head:                currentBranch,
		Base:                "main",
		Body:                prDescription,
		Draft:               true,
		MaintainerCanModify: true,
	}

	if githubToken == "" {
		return fmt.Errorf("GitHub token not provided in settings")
	}

	// Create PR using GitHub API
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls", owner, repoName)
	jsonData, err := json.Marshal(prRequest)
	if err != nil {
		return fmt.Errorf("error marshaling PR request: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("error creating PR: %s", string(body))
	}

	var prResponse GitHubPRResponse
	if err := json.NewDecoder(resp.Body).Decode(&prResponse); err != nil {
		return fmt.Errorf("error decoding PR response: %v", err)
	}

	// include the pr link in the response
	prLink := fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repoName, prResponse.Number)
	log.Printf("PR created successfully: %s", prLink)

	return nil
}

func getChanges(repo *git.Repository) (*Changes, error) {
	w, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	status, err := w.Status()
	if err != nil {
		return nil, err
	}

	var files []string
	for file := range status {
		files = append(files, file)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, err
	}

	currentBranch := head.Name().Short()
	branchChanges, err := getBranchChanges(repo, currentBranch, "main")
	if err != nil {
		return nil, fmt.Errorf("error getting branch changes: %v", err)
	}

	// Convert commits to messages
	var commits []string
	for _, commit := range branchChanges.Commits {
		commits = append(commits, commit.Message)
	}

	// Add any files from branch changes that aren't already included
	fileSet := make(map[string]struct{})
	for _, file := range files {
		fileSet[file] = struct{}{}
	}
	for _, file := range branchChanges.Files {
		if _, exists := fileSet[file]; !exists {
			files = append(files, file)
		}
	}

	return &Changes{
		Files:   files,
		Commits: commits,
		Summary: fmt.Sprintf("Changed files:\n%v\n\nCommits:\n%v", files, commits),
	}, nil
}

func formatChangesForPrompt(changes *Changes) string {
	return fmt.Sprintf("Changed files:\n%v\n\nRecent commits for context:\n%v",
		strings.Join(changes.Files, "\n"),
		strings.Join(changes.Commits, "\n"))
}

func GetGeminiModels(apiKey string) ([]string, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %v", err)
	}
	defer client.Close()

	iter := client.ListModels(ctx)
	var geminiModels []string

	for {
		model, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error listing models: %v", err)
		}

		// Include all available models
		geminiModels = append(geminiModels, model.Name)
	}

	if len(geminiModels) == 0 {
		return []string{"gemini-pro", "gemini-pro-vision"}, nil
	}

	return geminiModels, nil
}
