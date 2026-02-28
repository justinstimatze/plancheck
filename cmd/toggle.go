package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

func disabledFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".plancheck", "disabled")
}

func isDisabled() bool {
	_, err := os.Stat(disabledFile())
	return err == nil
}

type DisableCmd struct{}

func (c *DisableCmd) Run() error {
	path := disabledFile()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		return fmt.Errorf("failed to disable: %w", err)
	}
	fmt.Println("plancheck disabled")
	return nil
}

type EnableCmd struct{}

func (c *EnableCmd) Run() error {
	path := disabledFile()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to enable: %w", err)
	}
	fmt.Println("plancheck enabled")
	return nil
}
