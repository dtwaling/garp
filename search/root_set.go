package search

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StartRoot is one validated walk root. SuppliedPath preserves cleaned display
// spelling, LogicalPath is absolute, and PhysicalPath identifies the target.
type StartRoot struct {
	LogicalPath   string
	SuppliedPath  string
	PhysicalPath  string
	CanonicalKey  string
	Supplied      bool
	SuppliedOrder int
	// Reported is false when a nested root remains only because its supplied
	// ancestor cannot traverse into it.
	Reported bool
}

// StartRootOptions controls traversal-aware nested-root collapse.
type StartRootOptions struct {
	NoIgnore  bool
	PathScope []string
	UserGlobs []UserGlob
}

// PrepareStartRoots expands, validates, resolves, and deduplicates start roots.
func PrepareStartRoots(raw []string, retainNested bool) ([]StartRoot, error) {
	supplied := raw != nil
	if raw == nil {
		raw = []string{"."}
	}

	values := make([]string, 0, len(raw))
	for _, value := range raw {
		trimmed := stripMatchingQuotes(strings.TrimSpace(value))
		if trimmed == "" {
			return nil, fmt.Errorf("--startdir: path must not be empty")
		}
		if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
			values = append(values, trimmed)
			continue
		}
		segments, err := splitStartRootValue(value)
		if err != nil {
			return nil, err
		}
		values = append(values, segments...)
	}

	roots := make([]StartRoot, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for suppliedOrder, value := range values {
		cleaned, err := ValidateStartDir(value)
		if err != nil {
			return nil, err
		}
		logical, physical, err := resolveWalkRoot(cleaned)
		if err != nil {
			return nil, fmt.Errorf("invalid search root %q: %w", cleaned, err)
		}
		key := canonicalPathKey(physical)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, StartRoot{
			LogicalPath:   logical,
			SuppliedPath:  cleaned,
			PhysicalPath:  physical,
			CanonicalKey:  key,
			Supplied:      supplied,
			SuppliedOrder: suppliedOrder,
			Reported:      true,
		})
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("--startdir: no search roots were supplied")
	}
	return ApplyStartRootOptions(roots, retainNested, StartRootOptions{}), nil
}

// ApplyStartRootOptions refines prepared traversal roots without reparsing or
// resolving them again.
func ApplyStartRootOptions(roots []StartRoot, retainNested bool, options StartRootOptions) []StartRoot {
	roots = append([]StartRoot(nil), roots...)
	for index := range roots {
		roots[index].Reported = true
	}
	if retainNested {
		return roots
	}
	exemptionRetention := pathScopeCanExemptDirectChild(options.PathScope)

	// Compatibility reporting collapses every supplied descendant regardless
	// of which roots must remain for traversal.
	for childIndex, child := range roots {
		for parentIndex, parent := range roots {
			if childIndex == parentIndex || !parent.Supplied {
				continue
			}
			if strictPathDescendant(parent.PhysicalPath, child.PhysicalPath) {
				roots[childIndex].Reported = false
				break
			}
		}
	}

	// Evaluate shallow roots first. Only a retained ancestor can make a deeper
	// root redundant; a dropped intermediate root cannot cover descendants.
	order := make([]int, len(roots))
	for index := range roots {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		return physicalPathDepth(roots[order[left]].PhysicalPath) <
			physicalPathDepth(roots[order[right]].PhysicalPath)
	})
	keep := make([]bool, len(roots))
	for _, childIndex := range order {
		child := roots[childIndex]
		reachableFromKeptAncestor := false
		hasKeptAncestor := false
		for parentIndex, parent := range roots {
			if !keep[parentIndex] || !parent.Supplied ||
				!strictPathDescendant(parent.PhysicalPath, child.PhysicalPath) {
				continue
			}
			hasKeptAncestor = true
			if startRootCanReach(parent, child, options) {
				reachableFromKeptAncestor = true
				break
			}
		}
		keep[childIndex] = exemptionRetention && hasKeptAncestor || !reachableFromKeptAncestor
	}
	collapsed := make([]StartRoot, 0, len(roots))
	for index, root := range roots {
		if keep[index] {
			collapsed = append(collapsed, root)
		}
	}
	return collapsed
}

func physicalPathDepth(path string) int {
	path = strings.TrimPrefix(filepath.Clean(path), filepath.VolumeName(path))
	depth := 0
	for _, segment := range strings.Split(path, string(filepath.Separator)) {
		if segment != "" && segment != "." {
			depth++
		}
	}
	return depth
}

// ReportedStartRoots returns compatibility-facing roots, excluding nested
// traversal-only descriptors.
func ReportedStartRoots(roots []StartRoot) []StartRoot {
	reported := make([]StartRoot, 0, len(roots))
	for _, root := range roots {
		if root.Reported {
			reported = append(reported, root)
		}
	}
	return reported
}

func startRootCanReach(parent, child StartRoot, options StartRootOptions) bool {
	relative, err := filepath.Rel(parent.PhysicalPath, child.PhysicalPath)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	traversal, err := prepareRootTraversal(parent, options.PathScope, options.UserGlobs, options.NoIgnore)
	if err != nil {
		return false
	}
	parentChain, reachable := startRootTraversalState(traversal, relative)
	if !reachable || options.NoIgnore {
		return reachable
	}
	childTraversal, err := prepareRootTraversal(child, options.PathScope, options.UserGlobs, false)
	if err != nil {
		return false
	}
	return ignoreChainsEquivalent(parentChain, childTraversal.rootChain)
}

func startRootTraversalState(traversal preparedRootTraversal, relativeDirectory string) (ignoreChain, bool) {
	chain := traversal.rootChain
	currentPhysical := traversal.root.PhysicalPath
	currentLogical := traversal.root.LogicalPath
	relativeDirectory = filepath.Clean(relativeDirectory)
	if relativeDirectory == "." || relativeDirectory == "" {
		return chain, true
	}
	currentRelative := "."
	for depth, segment := range strings.Split(relativeDirectory, string(filepath.Separator)) {
		currentRelative = filepath.Join(currentRelative, segment)
		currentPhysical = filepath.Join(currentPhysical, segment)
		currentLogical = filepath.Join(currentLogical, segment)
		cacheKey := canonicalPathKey(currentRelative)
		if cached, exists := traversal.directoryCache[cacheKey]; exists {
			if !cached.reachable {
				return ignoreChain{}, false
			}
			chain = cached.chain
			continue
		}
		var descend bool
		chain, descend = decideTraversalDirectory(
			traversal,
			currentLogical,
			currentPhysical,
			segment,
			depth,
			chain,
		)
		traversal.directoryCache[cacheKey] = cachedTraversalDirectory{
			chain:     chain,
			reachable: descend,
		}
		if !descend {
			return ignoreChain{}, false
		}
	}
	return chain, true
}

func startRootCanProduceFile(traversal preparedRootTraversal, logicalPath string) bool {
	relative, err := filepath.Rel(traversal.root.LogicalPath, logicalPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	chain, reachable := startRootTraversalState(traversal, filepath.Dir(relative))
	if !reachable {
		return false
	}
	physicalPath := filepath.Join(traversal.root.PhysicalPath, relative)
	linked, linkErr := isDescendantLink(physicalPath)
	if linkErr != nil || linked {
		return false
	}
	if !traversal.noIgnore && ignoredByRules(chain.rules, physicalPath, false) {
		return false
	}
	return traversal.policy.MatchesFile(logicalPath)
}

func splitStartRootValue(raw string) ([]string, error) {
	var values []string
	start := 0
	var quote rune
	for index, char := range raw {
		switch {
		case quote != 0 && char == quote:
			quote = 0
		case quote == 0 && (char == '\'' || char == '"'):
			quote = char
		case quote == 0 && char == ',':
			value := stripMatchingQuotes(strings.TrimSpace(raw[start:index]))
			if value == "" {
				return nil, fmt.Errorf("--startdir: empty comma-separated segment")
			}
			values = append(values, value)
			start = index + 1
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("--startdir: unmatched quote")
	}
	value := stripMatchingQuotes(strings.TrimSpace(raw[start:]))
	if value == "" {
		return nil, fmt.Errorf("--startdir: empty comma-separated segment")
	}
	return append(values, value), nil
}

func strictPathDescendant(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// RetainNestedStartRoots reports whether a relative path-anchored policy needs
// each supplied root to retain its own matching context.
func RetainNestedStartRoots(pathScope []string, userGlobs []UserGlob) bool {
	for _, raw := range pathScope {
		if !strings.HasPrefix(raw, "!") && relativePathAnchored(raw) {
			return true
		}
	}
	for _, glob := range userGlobs {
		if !glob.Negated && relativePathAnchored(glob.Pattern) {
			return true
		}
	}
	return false
}

// PreserveNestedStartRoots reports whether preparation must defer nested-root
// collapse until path-policy reachability is evaluated.
func PreserveNestedStartRoots(pathScope []string, userGlobs []UserGlob) bool {
	if RetainNestedStartRoots(pathScope, userGlobs) || pathScopeCanExemptDirectChild(pathScope) {
		return true
	}
	for _, raw := range pathScope {
		if strings.HasPrefix(raw, "!") {
			return true
		}
	}
	for _, glob := range userGlobs {
		if glob.Negated {
			return true
		}
	}
	return false
}

func relativePathAnchored(raw string) bool {
	pattern := strings.ReplaceAll(stripMatchingQuotes(strings.TrimSpace(raw)), `\`, "/")
	if filepath.IsAbs(filepath.FromSlash(pattern)) {
		return false
	}
	return strings.HasPrefix(pattern, "./") || strings.Contains(pattern, "/")
}

// ValidateRootPolicies rejects absolute patterns that no prepared root contains.
func ValidateRootPolicies(roots []StartRoot, pathScope []string, userGlobs []UserGlob) error {
	patterns := make([]string, 0, len(pathScope)+len(userGlobs))
	for _, raw := range pathScope {
		patterns = append(patterns, strings.TrimPrefix(raw, "!"))
	}
	for _, glob := range userGlobs {
		patterns = append(patterns, glob.Pattern)
	}
	for _, raw := range patterns {
		pattern := filepath.FromSlash(strings.ReplaceAll(stripMatchingQuotes(strings.TrimSpace(raw)), `\`, "/"))
		if !filepath.IsAbs(pattern) {
			continue
		}
		contained := false
		for _, root := range roots {
			if _, ok := relativeAbsolutePattern(pattern, root.LogicalPath, root.PhysicalPath); ok {
				contained = true
				break
			}
		}
		if !contained {
			return fmt.Errorf("absolute path pattern %q is outside every search root", raw)
		}
	}
	return nil
}

func canonicalCandidateKey(root StartRoot, logicalPath string) string {
	relative, err := filepath.Rel(root.LogicalPath, logicalPath)
	if err != nil {
		return canonicalPathKey(logicalPath)
	}
	return canonicalPathKey(filepath.Join(root.PhysicalPath, relative))
}

func canonicalPathKeyForPlatform(path string, windows bool) string {
	key := filepath.Clean(path)
	if windows {
		key = strings.ToLower(key)
	}
	return key
}
