package sshconn

// Config holds SSH connection configuration for a single host.
type Config struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	AuthType    string `json:"auth_type"` // "key", "password"
	KeyPath     string `json:"key_path"`
	Password    string `json:"password"`
	ExecMode    string `json:"exec_mode"`    // "subscription", "own_plan"
	CLITool     string `json:"cli_tool"`     // "claude", "codex" (only for own_plan mode)
	AutoConnect bool   `json:"auto_connect"` // connect automatically on app startup
}
