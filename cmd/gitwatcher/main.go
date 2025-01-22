package main

import (
        "embed"
        "encoding/json"
        "fmt"
        "html/template"
        "log"
        "net/http"
        "path/filepath"
        "sync"
        "time"
        "os"

        git "github.com/go-git/go-git/v5"
        "gitwatcher/internal/gitops"
        "gitwatcher/internal/scheduler"
        "github.com/gorilla/mux"
        "github.com/rs/cors"
)

type Repository struct {
        Path     string    `json:"path"`
        Schedule string    `json:"schedule"`
        LastSync time.Time `json:"lastSync"`
        Status   *gitops.RepoStatus `json:"status,omitempty"`
}

type Settings struct {
        OllamaServer string `json:"ollamaServer"`
        OllamaModel  string `json:"ollamaModel"`
}

type AppState struct {
        Repositories map[string]*Repository `json:"repositories"`
        Settings     Settings              `json:"settings"`
        scheduler    *scheduler.Scheduler
        mu           sync.RWMutex
}

var state *AppState

func loadConfig() error {
        homeDir, err := os.UserHomeDir()
        if err != nil {
                return err
        }
        configDir := filepath.Join(homeDir, ".config", "gitwatcher")
        if err := os.MkdirAll(configDir, 0755); err != nil {
                return err
        }
        configPath := filepath.Join(configDir, "config.json")

        data, err := os.ReadFile(configPath)
        if err != nil {
                if os.IsNotExist(err) {
                        // Create default state if config doesn't exist
                        state = &AppState{
                                Repositories: make(map[string]*Repository),
                                Settings: Settings{
                                        OllamaServer: "http://localhost:11434",
                                        OllamaModel:  "llama2",
                                },
                                scheduler: scheduler.NewScheduler(),
                        }
                        return saveConfig()
                }
                return err
        }

        var config struct {
                Repositories map[string]Repository `json:"repositories"`
                Settings    Settings              `json:"settings"`
        }
        if err := json.Unmarshal(data, &config); err != nil {
                return err
        }

        // Create state from config
        state = &AppState{
                Repositories: make(map[string]*Repository),
                Settings:    config.Settings,
                scheduler:   scheduler.NewScheduler(),
        }

        // Set up repositories and their schedules
        for path, repo := range config.Repositories {
                state.Repositories[path] = &Repository{
                        Path:     repo.Path,
                        Schedule: repo.Schedule,
                }
                err = state.scheduler.AddTask(path, repo.Schedule, func() {
                        handleScheduledTask(path)
                })
                if err != nil {
                        log.Printf("Error setting up schedule for %s: %v", path, err)
                }
        }

        return nil
}

func saveConfig() error {
        homeDir, err := os.UserHomeDir()
        if err != nil {
                return err
        }
        configPath := filepath.Join(homeDir, ".config", "gitwatcher", "config.json")

        state.mu.RLock()
        defer state.mu.RUnlock()

        // Create config from state
        config := struct {
                Repositories map[string]Repository `json:"repositories"`
                Settings    Settings              `json:"settings"`
        }{
                Repositories: make(map[string]Repository),
                Settings:    state.Settings,
        }

        for path, repo := range state.Repositories {
                config.Repositories[path] = Repository{
                        Path:     repo.Path,
                        Schedule: repo.Schedule,
                }
        }

        data, err := json.MarshalIndent(config, "", "  ")
        if err != nil {
                return err
        }

        return os.WriteFile(configPath, data, 0644)
}

//go:embed templates
var templatesFS embed.FS

var templates *template.Template

func init() {
        var err error
        templates, err = template.ParseFS(templatesFS, "templates/*.html")
        if err != nil {
                log.Fatal(err)
        }
}

func main() {
        if err := loadConfig(); err != nil {
                log.Fatal(err)
        }

        r := mux.NewRouter()

        // API routes
        api := r.PathPrefix("/api").Subrouter()
        api.HandleFunc("/repositories", handleListRepositories).Methods("GET")
        api.HandleFunc("/repositories", handleAddRepository).Methods("POST")
        api.HandleFunc("/repositories/update", handleUpdateRepository).Methods("POST")
        api.HandleFunc("/repositories/commit", handleCommit).Methods("POST")
        api.HandleFunc("/repositories/push", handlePush).Methods("POST")
        api.HandleFunc("/repositories/pr", handleCreatePR).Methods("POST")
        api.HandleFunc("/settings", handleGetSettings).Methods("GET")
        api.HandleFunc("/settings", handleUpdateSettings).Methods("POST")

        // Web routes
        r.HandleFunc("/", handleHome).Methods("GET")
        r.HandleFunc("/settings", handleSettingsPage).Methods("GET")

        // Configure CORS for API routes
        c := cors.New(cors.Options{
                AllowedOrigins: []string{"*"},
                AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
                AllowedHeaders: []string{"*"},
        })

        // Start the scheduler
        state.scheduler.Start()
        defer state.scheduler.Stop()

        handler := c.Handler(r)
        log.Printf("Server starting on http://0.0.0.0:8082")
        log.Fatal(http.ListenAndServe("0.0.0.0:8082", handler))
}

type PageData struct {
        Page         string
        Repositories map[string]*Repository
        Settings     Settings
}

func handleHome(w http.ResponseWriter, r *http.Request) {
        state.mu.RLock()
        data := PageData{
                Page:         "home",
                Repositories: state.Repositories,
                Settings:     state.Settings,
        }
        state.mu.RUnlock()

        err := templates.ExecuteTemplate(w, "layout.html", data)
        if err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
        }
}

func handleSettingsPage(w http.ResponseWriter, r *http.Request) {
        state.mu.RLock()
        data := PageData{
                Page:         "settings",
                Repositories: state.Repositories,
                Settings:     state.Settings,
        }
        state.mu.RUnlock()

        err := templates.ExecuteTemplate(w, "layout.html", data)
        if err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
        }
}

func handleListRepositories(w http.ResponseWriter, r *http.Request) {
        state.mu.RLock()
        defer state.mu.RUnlock()

        json.NewEncoder(w).Encode(state.Repositories)
}

func handleAddRepository(w http.ResponseWriter, r *http.Request) {
        var repo Repository
        if err := json.NewDecoder(r.Body).Decode(&repo); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }

        absPath, err := filepath.Abs(repo.Path)
        if err != nil {
                http.Error(w, "Invalid path", http.StatusBadRequest)
                return
        }
        repo.Path = absPath

        _, err = git.PlainOpen(repo.Path)
        if err != nil {
                http.Error(w, "Invalid git repository path", http.StatusBadRequest)
                return
        }

        status, err := gitops.GetRepoStatus(repo.Path)
        if err != nil {
                http.Error(w, fmt.Sprintf("Error getting repo status: %v", err), http.StatusInternalServerError)
                return
        }
        repo.Status = status

        state.mu.Lock()
        defer state.mu.Unlock()

        state.Repositories[repo.Path] = &repo

        // Set up scheduler for the repository
        err = state.scheduler.AddTask(repo.Path, repo.Schedule, func() {
                handleScheduledTask(repo.Path)
        })
        if err != nil {
                http.Error(w, fmt.Sprintf("Error setting up schedule: %v", err), http.StatusInternalServerError)
                return
        }

        w.WriteHeader(http.StatusCreated)
}

func handleUpdateRepository(w http.ResponseWriter, r *http.Request) {
        var req struct {
                Path string `json:"path"`
        }
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        path := req.Path

        absPath, err := filepath.Abs(path)
        if err != nil {
                http.Error(w, "Invalid path", http.StatusBadRequest)
                return
        }

        repo, err := git.PlainOpen(absPath)
        if err != nil {
                http.Error(w, "Repository not found", http.StatusNotFound)
                return
        }

        // Perform fetch
        err = repo.Fetch(&git.FetchOptions{})
        if err != nil && err != git.NoErrAlreadyUpToDate {
                log.Printf("Warning: fetch error: %v", err)
        }

        // Get updated status
        status, err := gitops.GetRepoStatus(absPath)
        if err != nil {
                http.Error(w, fmt.Sprintf("Error getting repo status: %v", err), http.StatusInternalServerError)
                return
        }

        state.mu.Lock()
        if repo, exists := state.Repositories[absPath]; exists {
                repo.Status = status
                repo.LastSync = time.Now()
        }
        state.mu.Unlock()

        json.NewEncoder(w).Encode(status)
}

func handleCommit(w http.ResponseWriter, r *http.Request) {
        var req struct {
                Path string `json:"path"`
        }
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        path := req.Path

        absPath, err := filepath.Abs(path)
        if err != nil {
                http.Error(w, "Invalid path", http.StatusBadRequest)
                return
        }

        state.mu.RLock()
        settings := state.Settings
        state.mu.RUnlock()

        err = gitops.CommitChanges(absPath, settings.OllamaServer, settings.OllamaModel)
        if err != nil {
                http.Error(w, fmt.Sprintf("Error committing changes: %v", err), http.StatusInternalServerError)
                return
        }

        // Get updated status
        status, err := gitops.GetRepoStatus(absPath)
        if err != nil {
                http.Error(w, fmt.Sprintf("Error getting repo status: %v", err), http.StatusInternalServerError)
                return
        }

        json.NewEncoder(w).Encode(status)
}

func handlePush(w http.ResponseWriter, r *http.Request) {
        var req struct {
                Path string `json:"path"`
        }
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        path := req.Path

        absPath, err := filepath.Abs(path)
        if err != nil {
                http.Error(w, "Invalid path", http.StatusBadRequest)
                return
        }

        err = gitops.PushChanges(absPath)
        if err != nil {
                http.Error(w, fmt.Sprintf("Error pushing changes: %v", err), http.StatusInternalServerError)
                return
        }

        w.WriteHeader(http.StatusOK)
}

func handleCreatePR(w http.ResponseWriter, r *http.Request) {
        var req struct {
                Path string `json:"path"`
        }
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }
        // TODO: Implement PR creation using GitHub API
        w.WriteHeader(http.StatusNotImplemented)
}

func handleScheduledTask(repoPath string) {
        state.mu.RLock()
        repo, exists := state.Repositories[repoPath]
        settings := state.Settings
        state.mu.RUnlock()

        if !exists {
                log.Printf("Repository not found for scheduled task: %s", repoPath)
                return
        }

        status, err := gitops.GetRepoStatus(repoPath)
        if err != nil {
                log.Printf("Error getting repo status: %v", err)
                return
        }

        if !status.HasChanges {
                return
        }

        // Commit changes
        err = gitops.CommitChanges(repoPath, settings.OllamaServer, settings.OllamaModel)
        if err != nil {
                log.Printf("Error committing changes: %v", err)
                return
        }

        // Push changes
        err = gitops.PushChanges(repoPath)
        if err != nil {
                log.Printf("Error pushing changes: %v", err)
                return
        }

        state.mu.Lock()
        repo.LastSync = time.Now()
        repo.Status = status
        state.mu.Unlock()
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
        state.mu.RLock()
        defer state.mu.RUnlock()

        json.NewEncoder(w).Encode(state.Settings)
}

func handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
        var settings Settings
        if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
                http.Error(w, err.Error(), http.StatusBadRequest)
                return
        }

        state.mu.Lock()
        state.Settings = settings
        state.mu.Unlock()

        if err := saveConfig(); err != nil {
                http.Error(w, fmt.Sprintf("Error saving config: %v", err), http.StatusInternalServerError)
                return
        }

        w.WriteHeader(http.StatusOK)
}
