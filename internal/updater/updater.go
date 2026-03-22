package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/creativeprojects/go-selfupdate/update"
)

const (
	repo          = "camjac251/power-panel"
	checkInterval = 6 * time.Hour
)

// httpClient is used for GitHub API and asset downloads. Timeout prevents
// indefinite hangs if GitHub is slow or rate-limiting.
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// UpdateStatus represents the current state of the updater for UI rendering.
type UpdateStatus struct {
	CurrentVersion   string
	AvailableVersion string
	Checking         bool
	Applying         bool
	LastCheck        time.Time
	Error            string
	InContainer      bool
}

// Updater checks GitHub Releases for new versions and applies them.
type Updater struct {
	current   string
	autoApply bool
	inner     *selfupdate.Updater

	mu        sync.Mutex
	available *selfupdate.Release
	lastCheck time.Time
	checking  bool
	applying  bool
	lastErr   error

	onUpdate func() // called after binary replacement to trigger shutdown
}

// New creates an Updater that checks GitHub Releases for the given repo.
// The onUpdate callback is called after a successful binary replacement to
// trigger a graceful shutdown (systemd restarts with the new binary).
func New(currentVersion string, autoApply bool, onUpdate func()) (*Updater, error) {
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return nil, fmt.Errorf("creating github source: %w", err)
	}

	// No Validator set. We verify against GitHub's server-computed asset
	// digests instead of a checksums.txt file. See Apply for details.
	inner, err := selfupdate.NewUpdater(selfupdate.Config{
		Source: source,
	})
	if err != nil {
		return nil, fmt.Errorf("creating updater: %w", err)
	}

	return &Updater{
		current:   currentVersion,
		autoApply: autoApply,
		inner:     inner,
		onUpdate:  onUpdate,
	}, nil
}

// Run starts the background update loop. Waits 1 minute after startup before
// the first check, then checks every 6 hours. Auto-applies if configured.
func (u *Updater) Run(ctx context.Context) {
	slog.Info("updater started", "current", u.current, "auto_apply", u.autoApply, "interval", checkInterval)

	// Wait before first check so the server has time to start and users can
	// see the UI before a potential auto-update restart.
	select {
	case <-ctx.Done():
		return
	case <-time.After(1 * time.Minute):
	}

	u.checkAndMaybeApply(ctx)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.checkAndMaybeApply(ctx)
		}
	}
}

func (u *Updater) checkAndMaybeApply(ctx context.Context) {
	if err := u.Check(ctx); err != nil {
		slog.Warn("update check failed", "error", err)
		return
	}

	if !u.autoApply {
		return
	}

	u.mu.Lock()
	hasUpdate := u.available != nil
	u.mu.Unlock()

	if hasUpdate {
		if err := u.Apply(ctx); err != nil {
			slog.Error("update apply failed", "error", err)
		}
	}
}

// Check queries GitHub for a newer release.
func (u *Updater) Check(ctx context.Context) error {
	u.mu.Lock()
	u.checking = true
	u.lastErr = nil
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		u.checking = false
		u.lastCheck = time.Now()
		u.mu.Unlock()
	}()

	release, found, err := u.inner.DetectLatest(ctx, selfupdate.ParseSlug(repo))
	if err != nil {
		u.mu.Lock()
		u.lastErr = err
		u.mu.Unlock()
		return fmt.Errorf("detecting latest release: %w", err)
	}

	if !found || release.LessOrEqual(u.current) {
		slog.Debug("up to date", "current", u.current)
		u.mu.Lock()
		u.available = nil
		u.mu.Unlock()
		return nil
	}

	slog.Info("update available", "current", u.current, "available", release.Version())
	u.mu.Lock()
	u.available = release
	u.mu.Unlock()
	return nil
}

// Apply downloads the available update, verifies its SHA256 against GitHub's
// server-computed asset digest, replaces the binary, and triggers a restart.
// Concurrent calls are rejected.
func (u *Updater) Apply(ctx context.Context) error {
	u.mu.Lock()
	if u.applying {
		u.mu.Unlock()
		return fmt.Errorf("update already in progress")
	}
	release := u.available
	if release == nil {
		u.mu.Unlock()
		return fmt.Errorf("no update available")
	}
	u.applying = true
	u.lastErr = nil
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		u.applying = false
		u.mu.Unlock()
	}()

	setErr := func(err error) {
		u.mu.Lock()
		u.lastErr = err
		u.mu.Unlock()
	}

	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		setErr(err)
		return fmt.Errorf("getting executable path: %w", err)
	}

	tag := "v" + release.Version()
	slog.Info("applying update", "from", u.current, "to", release.Version(), "path", exe)

	// Fetch expected SHA256 from GitHub's server-computed asset digest.
	expectedHash, err := fetchAssetDigest(ctx, tag, release.AssetName)
	if err != nil {
		setErr(err)
		return fmt.Errorf("fetching asset digest: %w", err)
	}

	// Download the asset directly from the release download URL.
	assetURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, release.AssetName)
	data, err := downloadAsset(ctx, assetURL)
	if err != nil {
		setErr(err)
		return fmt.Errorf("downloading asset: %w", err)
	}

	// Apply with the expected checksum. update.Apply verifies the hash
	// before replacing the binary and rolls back on mismatch.
	err = update.Apply(bytes.NewReader(data), update.Options{
		TargetPath: exe,
		Checksum:   expectedHash,
	})
	if err != nil {
		setErr(err)
		return fmt.Errorf("applying update: %w", err)
	}

	slog.Info("update applied, restarting", "version", release.Version())

	u.mu.Lock()
	u.available = nil
	u.mu.Unlock()

	u.onUpdate()
	return nil
}

// Status returns the current updater state for UI rendering.
func (u *Updater) Status() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()

	s := UpdateStatus{
		CurrentVersion: u.current,
		Checking:       u.checking,
		Applying:       u.applying,
		LastCheck:      u.lastCheck,
	}
	if u.available != nil {
		s.AvailableVersion = u.available.Version()
	}
	if u.lastErr != nil {
		s.Error = u.lastErr.Error()
	}
	return s
}

// fetchAssetDigest calls the GitHub Releases API to get the server-computed
// SHA256 digest for a specific asset. The digest field is immutable and
// computed at upload time by GitHub.
func fetchAssetDigest(ctx context.Context, tag, assetName string) ([]byte, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decoding release: %w", err)
	}

	for _, asset := range release.Assets {
		if asset.Name == assetName {
			// Format: "sha256:<hex>"
			hexDigest, ok := strings.CutPrefix(asset.Digest, "sha256:")
			if !ok {
				return nil, fmt.Errorf("unexpected digest format: %s", asset.Digest)
			}
			return hex.DecodeString(hexDigest)
		}
	}
	return nil, fmt.Errorf("asset %q not found in release %s", assetName, tag)
}

// downloadAsset fetches a release asset via direct download URL.
func downloadAsset(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Limit to 100MB to prevent unbounded reads.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return nil, err
	}

	slog.Info("asset downloaded", "url", url, "size", len(data),
		"sha256", fmt.Sprintf("%x", sha256.Sum256(data)))
	return data, nil
}
