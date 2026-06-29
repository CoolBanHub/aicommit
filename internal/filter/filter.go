package filter

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type Options struct {
	MaxFileBytes int64
	Include      []string
	Exclude      []string
}

type Rules struct {
	maxFileBytes int64
	include      []string
	exclude      []string
}

type Decision struct {
	Path    string `json:"path"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

func NewRules(opts Options) Rules {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = 2 * 1024 * 1024
	}
	return Rules{
		maxFileBytes: opts.MaxFileBytes,
		include:      normalizePatterns(opts.Include),
		exclude:      normalizePatterns(opts.Exclude),
	}
}

func DefaultGitignorePatterns() []string {
	return []string{
		".env",
		".env.*",
		".npmrc",
		".pypirc",
		".netrc",
		"id_rsa",
		"id_rsa.*",
		"id_ed25519",
		"id_ed25519.*",
		"credentials",
		"credentials.json",
		"node_modules/",
		".next/",
		".nuxt/",
		"dist/",
		"build/",
		"coverage/",
		"target/",
		"vendor/",
		"*.7z",
		"*.a",
		"*.apk",
		"*.avi",
		"*.bin",
		"*.bmp",
		"*.bz2",
		"*.class",
		"*.dmg",
		"*.doc",
		"*.docx",
		"*.dylib",
		"*.eot",
		"*.exe",
		"*.gif",
		"*.gz",
		"*.heic",
		"*.ico",
		"*.jar",
		"*.jpeg",
		"*.key",
		"*.lockb",
		"*.mov",
		"*.mp3",
		"*.mp4",
		"*.o",
		"*.otf",
		"*.p12",
		"*.pfx",
		"*.pem",
		"*.ppt",
		"*.pptx",
		"*.rar",
		"*.sqlite",
		"*.sqlite3",
		"*.tar",
		"*.tgz",
		"*.ttf",
		"*.webm",
		"*.webp",
		"*.woff",
		"*.woff2",
		"*.xls",
		"*.xlsx",
		"*.xz",
		"*.zip",
	}
}

func (r Rules) Decide(repoRoot, relPath string) Decision {
	clean := normalizePath(relPath)
	if clean == "" {
		return Decision{Path: clean, Allowed: false, Reason: "empty path"}
	}
	if matchAny(r.include, clean) {
		return Decision{Path: clean, Allowed: true}
	}
	if matchAny(r.exclude, clean) {
		return Decision{Path: clean, Allowed: false, Reason: "excluded by pattern"}
	}
	if reason := defaultPathReason(clean); reason != "" {
		return Decision{Path: clean, Allowed: false, Reason: reason}
	}
	abs := filepath.Join(repoRoot, filepath.FromSlash(clean))
	info, err := os.Stat(abs)
	if err != nil {
		// Deleted files are decided by path only. If a tracked .env is deleted, the
		// path rule above still protects it by default.
		return Decision{Path: clean, Allowed: true}
	}
	if info.IsDir() {
		return Decision{Path: clean, Allowed: true}
	}
	if info.Size() > r.maxFileBytes {
		return Decision{Path: clean, Allowed: false, Reason: "file is larger than maxFileBytes"}
	}
	if looksBinary(abs) {
		return Decision{Path: clean, Allowed: false, Reason: "binary file"}
	}
	return Decision{Path: clean, Allowed: true}
}

func defaultPathReason(rel string) string {
	base := path.Base(rel)
	lowerBase := strings.ToLower(base)
	lower := strings.ToLower(rel)

	for _, component := range strings.Split(lower, "/") {
		switch component {
		case ".git", ".hg", ".svn", "node_modules", ".next", ".nuxt", "dist", "build", "coverage", "target", "vendor":
			return "generated or dependency directory"
		}
	}

	if lowerBase == ".env" || strings.HasPrefix(lowerBase, ".env.") {
		return "environment file"
	}
	switch lowerBase {
	case ".npmrc", ".pypirc", ".netrc", "id_rsa", "id_ed25519", "credentials", "credentials.json":
		return "credential file"
	}
	if strings.HasPrefix(lowerBase, "id_rsa.") || strings.HasPrefix(lowerBase, "id_ed25519.") {
		return "credential file"
	}

	ext := strings.TrimPrefix(strings.ToLower(path.Ext(lower)), ".")
	if _, ok := blockedExtensions[ext]; ok {
		return "binary or archive extension"
	}
	return ""
}

var blockedExtensions = map[string]struct{}{
	"7z": {}, "a": {}, "apk": {}, "avi": {}, "bin": {}, "bmp": {}, "bz2": {}, "class": {},
	"dmg": {}, "doc": {}, "docx": {}, "dylib": {}, "eot": {}, "exe": {}, "gif": {},
	"gz": {}, "heic": {}, "ico": {}, "jar": {}, "jpeg": {}, "key": {}, "lockb": {},
	"mov": {}, "mp3": {}, "mp4": {}, "o": {}, "otf": {}, "p12": {}, "pfx": {},
	"pem": {}, "ppt": {}, "pptx": {}, "rar": {}, "sqlite": {}, "sqlite3": {},
	"tar": {}, "tgz": {}, "ttf": {}, "webm": {}, "webp": {}, "woff": {}, "woff2": {}, "xls": {},
	"xlsx": {}, "xz": {}, "zip": {},
}

func looksBinary(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if n == 0 || err != nil {
		return false
	}
	sample := buf[:n]
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	return !utf8.Valid(sample)
}

func normalizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = normalizePath(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = filepath.ToSlash(p)
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	if p == "." {
		return ""
	}
	return p
}

func matchAny(patterns []string, rel string) bool {
	for _, pattern := range patterns {
		if matchPattern(pattern, rel) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, rel string) bool {
	if pattern == rel {
		return true
	}
	if strings.HasSuffix(pattern, "/") && strings.HasPrefix(rel, strings.TrimSuffix(pattern, "/")+"/") {
		return true
	}
	if !strings.Contains(pattern, "/") && path.Base(rel) == pattern {
		return true
	}
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	if strings.Contains(pattern, "**") {
		return matchDoubleStar(pattern, rel)
	}
	return false
}

func matchDoubleStar(pattern, rel string) bool {
	parts := strings.Split(pattern, "**")
	if len(parts) == 1 {
		return pattern == rel
	}
	pos := 0
	if parts[0] != "" {
		if !strings.HasPrefix(rel, parts[0]) {
			return false
		}
		pos = len(parts[0])
	}
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		idx := strings.Index(rel[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
	}
	if last := parts[len(parts)-1]; last != "" {
		return strings.HasSuffix(rel, last)
	}
	return true
}
