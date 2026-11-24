package auth

import (
	"fmt"

	"yansu-agent/internal/client"
	"yansu-agent/internal/config"
)

// Logout clears the saved token
func Logout() error {
	token, err := config.LoadToken()
	if err != nil {
		return nil // Nothing to clear
	}

	if token == "" {
		fmt.Println("You are not logged in.")
		return nil
	}

	if err := config.ClearToken(); err != nil {
		return fmt.Errorf("failed to clear token: %w", err)
	}

	fmt.Println("✓ Logged out successfully")
	return nil
}

// Status shows the current authentication status
func Status() error {
	fmt.Println(client.ErrRetired)
	return nil
}
