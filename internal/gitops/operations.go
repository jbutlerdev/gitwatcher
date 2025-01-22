package gitops

import (
        "bytes"
        "encoding/json"
        "fmt"
        "net/http"
        "time"

        "github.com/go-git/go-git/v5"
        "github.com/go-git/go-git/v5/plumbing"
        "github.com/go-git/go-git/v5/plumbing/object"
)

type RepoStatus struct {
        HasChanges    bool     `json:"hasChanges"`
        ChangedFiles  []string `json:"changedFiles"`
        CurrentBranch string   `json:"currentBranch"`
        IsClean      bool     `json:"isClean"`
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
                IsClean:      status.IsClean(),
        }, nil
}

func CommitChanges(path string, ollamaServer string, ollamaModel string) error {
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
                return fmt.Errorf("no changes to commit")
        }

        // Add all changes
        _, err = w.Add(".")
        if err != nil {
                return err
        }

        // Get changed files for commit message context
        var changedFiles []string
        for file := range status {
                changedFiles = append(changedFiles, file)
        }

        // Generate commit message using Ollama
        message, err := generateCommitMessage(changedFiles, ollamaServer, ollamaModel)
        if err != nil {
                return err
        }

        // Create commit
        _, err = w.Commit(message, &git.CommitOptions{
                Author: &object.Signature{
                        Name:  "GitWatcher",
                        Email: "gitwatcher@local",
                        When:  time.Now(),
                },
        })

        return err
}

func PushChanges(path string) error {
        repo, err := git.PlainOpen(path)
        if err != nil {
                return err
        }

        return repo.Push(&git.PushOptions{})
}

func generateCommitMessage(changedFiles []string, ollamaServer string, ollamaModel string) (string, error) {
        prompt := fmt.Sprintf("Generate a concise commit message for the following changed files:\n%v\n"+
                "The commit message should follow conventional commits format and be under 72 characters.", changedFiles)

        reqBody := OllamaRequest{
                Model: ollamaModel,
                Messages: []struct {
                        Role    string "json:\"role\""
                        Content string "json:\"content\""
                }{
                        {
                                Role:    "user",
                                Content: prompt,
                        },
                },
        }

        jsonBody, err := json.Marshal(reqBody)
        if err != nil {
                return "", err
        }

        resp, err := http.Post(ollamaServer+"/api/chat", "application/json", bytes.NewBuffer(jsonBody))
        if err != nil {
                return "", err
        }
        defer resp.Body.Close()

        var ollamaResp OllamaResponse
        if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
                return "", err
        }

        return ollamaResp.Message.Content, nil
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