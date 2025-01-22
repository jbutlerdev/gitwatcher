# GitWatcher

A web application that watches git repositories and automates commits, pushes, and pull requests.

## Features

- Watch local git repositories
- Schedule automatic commits and pushes
- Generate commit messages using Ollama
- Create draft pull requests automatically
- Modern dark theme UI

## Prerequisites

- Go 1.21 or later
- Node.js 18 or later
- Ollama server running locally (or configured remotely)

## Setup

### Backend

1. Navigate to the project root:
   ```bash
   cd gitwatcher
   ```

2. Install Go dependencies:
   ```bash
   go mod tidy
   ```

3. Run the backend server:
   ```bash
   go run main.go
   ```

The backend server will start on port 55367.

### Frontend

1. Navigate to the frontend directory:
   ```bash
   cd frontend
   ```

2. Install dependencies:
   ```bash
   npm install
   ```

3. Run the development server:
   ```bash
   npm run dev
   ```

The frontend will be available at http://localhost:51263

## Configuration

- Ollama settings can be configured through the frontend settings page
- Repository schedules can be set using cron syntax when adding or editing a repository