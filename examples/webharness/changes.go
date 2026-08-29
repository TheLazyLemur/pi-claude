package main

import (
	"fmt"
	"strings"
)

// fileChange is one file this session has touched, as git sees it.
type fileChange struct {
	Path   string
	Status string // added, modified, deleted, renamed
	Added  int
	Del    int
}

// changes reports what a session has done to its checkout, against the base it
// started from. A worktree session compares to the branch it forked off; a
// session working directly in a project compares to the working tree it found.
func (s *Session) changes() ([]fileChange, error) {
	repo := repoRoot(s.Root)
	if repo == "" {
		return nil, nil
	}

	base := "HEAD"
	numstat, err := run(s.Root, "git", "diff", "--numstat", base)
	if err != nil {
		return nil, err
	}
	status, err := run(s.Root, "git", "diff", "--name-status", base)
	if err != nil {
		return nil, err
	}
	untracked, _ := run(s.Root, "git", "ls-files", "--others", "--exclude-standard")

	kinds := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		kinds[parts[len(parts)-1]] = statusWord(parts[0])
	}

	var out []fileChange
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		out = append(out, fileChange{
			Path:   parts[2],
			Status: kinds[parts[2]],
			Added:  atoi(parts[0]),
			Del:    atoi(parts[1]),
		})
	}

	// Files git has never seen do not appear in a diff, but they are very much
	// part of what the agent did.
	for _, path := range strings.Fields(untracked) {
		if strings.HasPrefix(path, ".worktrees/") {
			continue
		}
		out = append(out, fileChange{Path: path, Status: "added", Added: countLines(s.Root, path)})
	}
	return out, nil
}

// diffFor returns the patch for one file, ready to render.
func (s *Session) diffFor(path string) []hunk {
	before, err := run(s.Root, "git", "show", "HEAD:"+path)
	if err != nil {
		before = ""
	}
	after, err := run(s.Root, "cat", path)
	if err != nil {
		after = ""
	}
	return unifiedDiff(before, after)
}

// mergeInto folds a worktree session's branch back into the branch it came
// from, without deleting anything: the worktree stays until you remove it.
func (s *Session) mergeInto() (string, error) {
	if !s.Worktree || s.Branch == "" {
		return "", fmt.Errorf("this session is not on a branch of its own")
	}

	repo := repoRoot(s.Root)
	if repo == "" {
		return "", fmt.Errorf("no repository here")
	}

	// The main checkout is the first worktree git lists.
	list, err := run(repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	main := ""
	for _, line := range strings.Split(list, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			main = strings.TrimPrefix(line, "worktree ")
			break
		}
	}
	if main == "" {
		return "", fmt.Errorf("could not find the main checkout")
	}

	if _, err := run(s.Root, "git", "add", "-A"); err != nil {
		return "", err
	}
	if _, err := run(s.Root, "git", "-c", "user.name=console", "-c", "user.email=console@localhost",
		"commit", "-m", commitMessage(s.Title)); err != nil {
		// Nothing staged is fine; the branch may already be committed.
		if !strings.Contains(err.Error(), "nothing to commit") {
			return "", err
		}
	}

	out, err := run(main, "git", "merge", "--no-ff", "-m", "merge "+s.Branch, s.Branch)
	if err != nil {
		return "", fmt.Errorf("merge failed, the branch is still there: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func commitMessage(title string) string {
	title = strings.TrimSpace(strings.Split(title, "\n")[0])
	if title == "" {
		return "work from a console session"
	}
	if len(title) > 72 {
		title = title[:69] + "..."
	}
	return title
}

func statusWord(code string) string {
	switch {
	case strings.HasPrefix(code, "A"):
		return "added"
	case strings.HasPrefix(code, "D"):
		return "deleted"
	case strings.HasPrefix(code, "R"):
		return "renamed"
	default:
		return "modified"
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func countLines(dir, path string) int {
	out, err := run(dir, "cat", path)
	if err != nil {
		return 0
	}
	return len(strings.Split(strings.TrimRight(out, "\n"), "\n"))
}
