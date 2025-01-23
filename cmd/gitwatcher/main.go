package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitwatcher/internal/gitops"
	"gitwatcher/internal/scheduler"

	git "github.com/go-git/go-git/v5"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

type Repository struct {
	Path     string             `json:"path"`
	Schedule string             `json:"schedule"`
	LastSync time.Time          `json:"lastSync"`
	Status   *gitops.RepoStatus `json:"status,omitempty"`
}

func (r *Repository) GetStatus() error {
	status, err := gitops.GetRepoStatus(r.Path)
	if err != nil {
		return err
	}
	r.Status = status
	r.LastSync = time.Now()
	return nil
}

type Settings struct {
	OllamaServer string `json:"ollamaServer"`
	OllamaModel  string `json:"ollamaModel"`
	GitHubToken  string `json:"githubToken"`
	AIService    string `json:"aiService"`
	GeminiAPIKey string `json:"geminiAPIKey"`
	GeminiModel  string `json:"geminiModel"`
}

func (s *Settings) GetAIService() gitops.AIService {
	if s.AIService == "gemini" {
		return gitops.AIService{
			Server: "",
			Model:  s.GeminiModel,
			Type:   s.AIService,
			APIKey: s.GeminiAPIKey,
		}
	}
	return gitops.AIService{
		Server: s.OllamaServer,
		Model:  s.OllamaModel,
		Type:   s.AIService,
		APIKey: "",
	}
}

type AppState struct {
	Repositories map[string]*Repository `json:"repositories"`
	Settings     Settings               `json:"settings"`
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
		Settings     Settings              `json:"settings"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	// Create state from config
	state = &AppState{
		Repositories: make(map[string]*Repository),
		Settings:     config.Settings,
		scheduler:    scheduler.NewScheduler(),
	}

	// Set up repositories and their schedules
	for path, repo := range config.Repositories {
		r := &Repository{
			Path:     repo.Path,
			Schedule: repo.Schedule,
		}
		err := r.GetStatus()
		if err != nil {
			log.Printf("Error getting repo status: %v", err)
		}
		state.Repositories[path] = r
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
		Settings     Settings              `json:"settings"`
	}{
		Repositories: make(map[string]Repository),
		Settings:     state.Settings,
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
	api.HandleFunc("/gemini/models", handleGeminiModels).Methods("GET")

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
	log.Printf("Adding repository: %v", r.Body)

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

	log.Printf("Getting repo status for %s", repo.Path)

	status, err := gitops.GetRepoStatus(repo.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error getting repo status: %v", err), http.StatusInternalServerError)
		return
	}
	repo.Status = status

	state.mu.Lock()

	state.Repositories[repo.Path] = &repo

	state.mu.Unlock()

	log.Printf("Adding scheduler task for %s", repo.Path)

	// Set up scheduler for the repository
	err = state.scheduler.AddTask(repo.Path, repo.Schedule, func() {
		handleScheduledTask(repo.Path)
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Error setting up schedule: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Saving config")
	err = saveConfig()
	if err != nil {
		log.Printf("Error saving config: %v", err)
		http.Error(w, fmt.Sprintf("Error saving config: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	log.Printf("Repository added successfully")
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

	// Perform fetch
	err = gitops.FetchRepository(absPath)
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
	defer state.mu.RUnlock()

	settings := &state.Settings

	err = gitops.CommitChanges(absPath, settings.GetAIService())
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

	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	state.mu.RLock()
	defer state.mu.RUnlock()

	settings := &state.Settings

	err = gitops.CreateDraftPR(absPath, settings.GetAIService(), settings.GitHubToken)
	if err != nil {
		log.Printf("Error creating PR: %v", err)
		http.Error(w, fmt.Sprintf("Error creating PR: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func handleScheduledTask(repoPath string) {
	state.mu.RLock()
	defer state.mu.RUnlock()

	repo, exists := state.Repositories[repoPath]
	settings := &state.Settings

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
	err = gitops.CommitChanges(repoPath, settings.GetAIService())
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

	err = gitops.CreateDraftPR(repoPath, settings.GetAIService(), settings.GitHubToken)
	if err != nil {
		log.Printf("Error creating PR: %v", err)
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	repo.LastSync = time.Now()
	repo.Status = status
	state.Repositories[repoPath] = repo
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

func handleGeminiModels(w http.ResponseWriter, r *http.Request) {
	state.mu.RLock()
	settings := state.Settings
	state.mu.RUnlock()

	if settings.GeminiAPIKey == "" {
		http.Error(w, "Gemini API key not configured", http.StatusBadRequest)
		return
	}

	models, err := gitops.GetGeminiModels(settings.GeminiAPIKey)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error fetching Gemini models: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(models)
}
