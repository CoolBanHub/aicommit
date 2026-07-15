package gitx

import (
	"context"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

// CompactDetectedGitignorePatterns replaces concrete paths with the highest
// directory wildcard that covers only those paths in the current worktree and
// index. Explicit allow and .gitignore negation patterns prevent broader rules.
func CompactDetectedGitignorePatterns(ctx context.Context, repo string, patterns, allowPatterns []string) []string {
	if len(patterns) < 2 {
		return append([]string{}, patterns...)
	}

	type detectedPattern struct {
		original  string
		path      string
		groupable bool
	}

	items := make([]detectedPattern, 0, len(patterns))
	detected := map[string]struct{}{}
	ungroupable := map[string]struct{}{}
	for _, pattern := range patterns {
		clean, ok := concreteDetectedPath(pattern)
		item := detectedPattern{original: pattern, path: clean}
		if ok {
			info, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(clean)))
			if err == nil && !info.IsDir() {
				item.groupable = true
				detected[clean] = struct{}{}
			} else {
				ungroupable[clean] = struct{}{}
			}
		}
		items = append(items, item)
	}
	if len(detected) < 2 {
		return append([]string{}, patterns...)
	}

	detectedBelow := map[string]int{}
	for file := range detected {
		for dir := pathpkg.Dir(file); dir != "." && dir != "/"; dir = pathpkg.Dir(dir) {
			detectedBelow[dir]++
		}
	}

	candidates := map[string]bool{}
	for dir, count := range detectedBelow {
		if count >= 2 && gitignoreDirectoryPatternIsLiteral(dir) && dir != ".git" && !strings.HasPrefix(dir, ".git/") {
			candidates[dir] = true
		}
	}
	if len(candidates) == 0 {
		return append([]string{}, patterns...)
	}
	roots := topmostCandidateDirectories(candidates)

	for file := range ungroupable {
		invalidateContainingCandidates(file, false, candidates)
	}
	trackedArgs := []string{"--literal-pathspecs", "ls-files", "-z", "--"}
	trackedArgs = append(trackedArgs, roots...)
	trackedData, err := runBytes(ctx, repo, trackedArgs...)
	if err != nil {
		return append([]string{}, patterns...)
	}
	for _, file := range splitZ(trackedData) {
		file = filepath.ToSlash(file)
		if _, ok := detected[file]; !ok {
			invalidateContainingCandidates(file, false, candidates)
		}
	}
	preservePatterns := append([]string{}, allowPatterns...)
	preservePatterns = append(preservePatterns, gitignoreNegationPatterns(repo)...)
	for dir := range candidates {
		for _, pattern := range preservePatterns {
			if preservePatternMayAffectDirectory(pattern, dir) {
				candidates[dir] = false
				break
			}
		}
	}

	for _, root := range roots {
		absoluteRoot := filepath.Join(repo, filepath.FromSlash(root))
		walkErr := filepath.Walk(absoluteRoot, func(current string, info os.FileInfo, walkErr error) error {
			rel, err := filepath.Rel(repo, current)
			if err != nil {
				invalidateCandidateSubtree(root, candidates)
				return filepath.SkipAll
			}
			rel = filepath.ToSlash(rel)
			if walkErr != nil {
				invalidateCandidateSubtree(root, candidates)
				return filepath.SkipAll
			}
			if info.IsDir() {
				if rel != root && detectedBelow[rel] == 0 {
					invalidateContainingCandidates(rel, true, candidates)
					return filepath.SkipDir
				}
				return nil
			}
			if _, ok := detected[rel]; !ok {
				invalidateContainingCandidates(rel, false, candidates)
			}
			return nil
		})
		if walkErr != nil {
			invalidateCandidateSubtree(root, candidates)
		}
	}

	selected := selectHighestSafeDirectories(candidates)
	if len(selected) == 0 {
		return append([]string{}, patterns...)
	}

	emitted := map[string]struct{}{}
	compacted := make([]string, 0, len(items))
	for _, item := range items {
		group := ""
		if item.groupable {
			group = selectedDirectoryForPath(item.path, selected)
		}
		if group == "" {
			compacted = append(compacted, item.original)
			continue
		}
		if _, ok := emitted[group]; ok {
			continue
		}
		emitted[group] = struct{}{}
		compacted = append(compacted, group+"/*")
	}
	return compacted
}

func gitignoreDirectoryPatternIsLiteral(dir string) bool {
	if strings.ContainsAny(dir, "*?[\\\n\r") {
		return false
	}
	for _, component := range strings.Split(dir, "/") {
		if component != strings.TrimSpace(component) {
			return false
		}
	}
	return true
}

func concreteDetectedPath(pattern string) (string, bool) {
	clean := filepath.ToSlash(pattern)
	if clean != strings.TrimSpace(clean) {
		return "", false
	}
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.TrimPrefix(clean, "/")
	clean = pathpkg.Clean(clean)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || pathpkg.IsAbs(clean) {
		return "", false
	}
	return clean, true
}

func invalidateCandidateSubtree(root string, candidates map[string]bool) {
	for dir := range candidates {
		if dir == root || strings.HasPrefix(dir, root+"/") {
			candidates[dir] = false
		}
	}
}

func invalidateContainingCandidates(item string, itemIsDirectory bool, candidates map[string]bool) {
	dir := pathpkg.Dir(item)
	if itemIsDirectory {
		dir = item
	}
	for dir != "." && dir != "/" {
		if _, ok := candidates[dir]; ok {
			candidates[dir] = false
		}
		dir = pathpkg.Dir(dir)
	}
}

func preservePatternMayAffectDirectory(pattern, dir string) bool {
	pattern = strings.TrimSpace(pattern)
	if strings.Contains(pattern, "\\") {
		return true
	}
	pattern = filepath.ToSlash(pattern)
	pattern = strings.TrimPrefix(pattern, "!")
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		return true
	}
	if meta := strings.IndexAny(pattern, "*?["); meta >= 0 {
		staticPrefix := pattern[:meta]
		slash := strings.LastIndex(staticPrefix, "/")
		if slash < 0 {
			return true
		}
		pattern = staticPrefix[:slash]
	}
	pattern = strings.TrimSuffix(pattern, "/")
	pattern = pathpkg.Clean(pattern)
	if pattern == "." || pattern == "" {
		return true
	}
	return pattern == dir ||
		strings.HasPrefix(pattern, dir+"/") ||
		strings.HasPrefix(dir, pattern+"/")
}

func gitignoreNegationPatterns(repo string) []string {
	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "!") && len(line) > 1 {
			patterns = append(patterns, strings.TrimPrefix(line, "!"))
		}
	}
	return patterns
}

func topmostCandidateDirectories(candidates map[string]bool) []string {
	var dirs []string
	for dir := range candidates {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		leftDepth := strings.Count(dirs[i], "/")
		rightDepth := strings.Count(dirs[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return dirs[i] < dirs[j]
	})
	var roots []string
	for _, dir := range dirs {
		if selectedDirectoryForPath(dir+"/entry", roots) == "" {
			roots = append(roots, dir)
		}
	}
	return roots
}

func selectHighestSafeDirectories(candidates map[string]bool) []string {
	var dirs []string
	for dir, safe := range candidates {
		if safe {
			dirs = append(dirs, dir)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		leftDepth := strings.Count(dirs[i], "/")
		rightDepth := strings.Count(dirs[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return dirs[i] < dirs[j]
	})
	var selected []string
	for _, dir := range dirs {
		if selectedDirectoryForPath(dir+"/entry", selected) == "" {
			selected = append(selected, dir)
		}
	}
	return selected
}

func selectedDirectoryForPath(file string, directories []string) string {
	for _, dir := range directories {
		if strings.HasPrefix(file, dir+"/") {
			return dir
		}
	}
	return ""
}
