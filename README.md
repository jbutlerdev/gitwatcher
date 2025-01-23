# GitWatcher

A web application that watches git repositories and automates commits, pushes, and pull requests.

## Features

- Watch local git repositories
- Schedule automatic commits and pushes
- Generate commit messages using AI
- Create draft pull requests automatically
- Modern dark theme UI

## Prerequisites

- Go 1.21 or later
- Ollama server (or gemini API Key)

## Setup

### Backend

1. Navigate to the project root:
   ```bash
   cd gitwatcher
   ```

2. Build the project:
   ```bash
   make
   ```

3. Run the backend server:
   ```bash
   ./gitwatcher
   ```

The backend server will start on port 8082.

## Configuration

- Ollama and Gemini settings can be configured through the frontend settings page
- Repository schedules can be set using cron syntax when adding or editing a repository
