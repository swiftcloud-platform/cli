package ssh

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client represents an SSH client for Proxmox host access
type Client struct {
	client *ssh.Client
	host   string
}

// Config holds SSH connection configuration
type Config struct {
	Host           string
	Port           int
	Username       string
	Password       string
	PrivateKeyPath string
	Insecure       bool
}

// NewClient creates a new SSH client
func NewClient(cfg Config) (*Client, error) {
	sshConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	if cfg.Password != "" {
		sshConfig.Auth = append(sshConfig.Auth, ssh.Password(cfg.Password))
	}

	if cfg.PrivateKeyPath != "" {
		key, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read private key: %w", err)
		}

		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
		sshConfig.Auth = append(sshConfig.Auth, ssh.PublicKeys(signer))
	}

	if len(sshConfig.Auth) == 0 {
		return nil, fmt.Errorf("no authentication method provided (password or private key required)")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	if cfg.Port == 0 {
		addr = fmt.Sprintf("%s:22", cfg.Host)
	}

	client, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	return &Client{
		client: client,
		host:   cfg.Host,
	}, nil
}

// Close closes the SSH connection
func (c *Client) Close() error {
	return c.client.Close()
}

// Run executes a command on the remote host and returns stdout, stderr, and error
func (c *Client) Run(command string) (string, string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	err = session.Run(command)

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

// RunWithOutput executes a command and streams output to the provided writers
func (c *Client) RunWithOutput(command string) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	return session.Run(command)
}
