// Package doctorapi provides types and client for the cloud doctor API.
package doctorapi

import "time"

// DoctorContextResponse contains server-side context for client troubleshooting.
type DoctorContextResponse struct {
	User            *UserInfo  `json:"user,omitempty"`
	Repositories    []RepoInfo `json:"repositories"`
	Teams           []TeamInfo `json:"teams"`
	Endpoints       Endpoints  `json:"endpoints"`
	ExpectedEnvVars []EnvVar   `json:"expected_env_vars"`
	Server          ServerInfo `json:"server"`
}

// UserInfo contains basic user information.
type UserInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	Tier  string `json:"tier"`
}

// RepoInfo contains repository information.
type RepoInfo struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	Teams     []string  `json:"teams,omitempty"`
	Git       *GitInfo  `json:"git,omitempty"`
}

// GitInfo contains git-specific information.
type GitInfo struct {
	Provider      string `json:"provider,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	RemoteURL     string `json:"remote_url,omitempty"`
}

// TeamInfo contains team information.
type TeamInfo struct {
	ID       string   `json:"id"`
	Slug     string   `json:"slug"`
	Name     string   `json:"name"`
	Role     string   `json:"role,omitempty"`
	Git      *GitInfo `json:"git,omitempty"`
	NormsURL string   `json:"norms_url,omitempty"`
}

// Endpoints contains integration endpoints.
type Endpoints struct {
	API       string `json:"api"`
	Auth      string `json:"auth"`
	GitLab    string `json:"gitlab,omitempty"`
	WebSocket string `json:"websocket,omitempty"`
}

// EnvVar describes an expected environment variable.
type EnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Example     string `json:"example,omitempty"`
}

// ServerInfo contains server metadata.
type ServerInfo struct {
	Version     string `json:"version"`
	Environment string `json:"environment"`
	Timestamp   string `json:"timestamp"`
}
