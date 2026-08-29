package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Workspace is a project the agent can be pointed at.
type Workspace struct {
	ID     string
	Root   string
	Name   string
	IsRepo bool
}

// Store holds every workspace and every session across them.
type Store struct {
	mu         sync.Mutex
	workspaces []*Workspace
	byID       map[string]*Workspace
	sessions   map[string]*Session
	order      []string
	seq        int
}

func NewStore() *Store {
	return &Store{byID: map[string]*Workspace{}, sessions: map[string]*Session{}}
}

func (s *Store) AddWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := newWorkspace(abs); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, w := range s.workspaces {
		if w.Root == abs {
			return w, nil
		}
	}

	w := &Workspace{
		ID:     slug(filepath.Base(abs)) + "-" + fmt.Sprint(len(s.workspaces)+1),
		Root:   abs,
		Name:   filepath.Base(abs),
		IsRepo: repoRoot(abs) != "",
	}
	s.workspaces = append(s.workspaces, w)
	s.byID[w.ID] = w
	return w, nil
}

func (s *Store) Workspaces() []*Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Workspace(nil), s.workspaces...)
}

func (s *Store) Workspace(id string) *Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

func (s *Store) Session(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// Sessions returns every session, newest first.
func (s *Store) Sessions() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Session, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.sessions[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func (s *Store) put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	s.order = append(s.order, sess.ID)
}

func (s *Store) nextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("s%d", s.seq)
}

// ---- git worktrees ---------------------------------------------------------

// repoRoot returns the git toplevel for a directory, or "" when it is not a
// repository.
func repoRoot(dir string) string {
	out, err := run(dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// addWorktree branches off HEAD into its own checkout, so two sessions working
// on the same project cannot tread on each other.
func addWorktree(repo, name string) (path, branch string, err error) {
	branch = "console/" + name
	path = filepath.Join(repo, ".worktrees", name)

	excludeWorktrees(repo)

	if _, err := run(repo, "git", "worktree", "add", "-b", branch, path); err != nil {
		// A branch of that name may already exist from an earlier session.
		if _, retry := run(repo, "git", "worktree", "add", path, branch); retry != nil {
			return "", "", fmt.Errorf("git worktree add: %w", err)
		}
	}
	return path, branch, nil
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %s", name, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = notSlug.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s, "-")
}

// sessionName turns a first message into something short enough to name a
// branch and a sidebar row.
func sessionName(prompt string) string {
	words := strings.Fields(prompt)
	if len(words) > 6 {
		words = words[:6]
	}
	name := slug(strings.Join(words, " "))
	if name == "" {
		name = "session"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

func stamp(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2 Jan")
	}
}

// excludeWorktrees keeps .worktrees out of git status without touching a
// tracked .gitignore. info/exclude is local to the clone, which is where a
// tool's own scratch directory belongs.
func excludeWorktrees(repo string) {
	gitDir, err := run(repo, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return
	}
	dir := strings.TrimSpace(gitDir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo, dir)
	}

	path := filepath.Join(dir, "info", "exclude")
	body, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(body), ".worktrees/") {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprint(f, "\n# sessions started by the console\n.worktrees/\n")
}
