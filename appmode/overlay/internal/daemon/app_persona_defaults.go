package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"agentdc/internal/store"
)

const (
	appPersonaDefaultsEnv          = "AGENTDC_ZALO_PERSONA_DEFAULT_DIR"
	appPersonaManifestName         = "build-manifest.json"
	appPersonaMaxManifestBytes     = 1 << 20
	appPersonaMaxDefaultFileBytes  = 1 << 20
	appPersonaMaxDefaultTreeBytes  = 4 << 20
	appPersonaMaxDefaultFiles      = 64
	appPersonaMaxRelativePathBytes = 512
	appPersonaMaxLegacyHashes      = 8
	appPersonaManifestTreeDomain   = "agentdc/persona-default-tree/v1\x00"
)

var (
	errAppPersonaDefaultsInvalid  = errors.New("packaged Persona defaults are invalid")
	errAppPersonaRepairRequired   = errors.New("working Persona requires repair")
	appPackagedPersonaDisplayName = func(st *store.Store) (string, error) {
		return st.AgentDisplayName()
	}
)

type appPersonaDefaults struct {
	displayName string
	persona     []byte
	files       map[string][]byte
	legacy      map[string]struct{}
}

func (defaults appPersonaDefaults) clone() appPersonaDefaults {
	cloned := appPersonaDefaults{
		displayName: defaults.displayName,
		persona:     bytes.Clone(defaults.persona),
		files:       make(map[string][]byte, len(defaults.files)),
		legacy:      make(map[string]struct{}, len(defaults.legacy)),
	}
	for path, content := range defaults.files {
		cloned.files[path] = bytes.Clone(content)
	}
	for digest := range defaults.legacy {
		cloned.legacy[digest] = struct{}{}
	}
	return cloned
}

type appPersonaDefaultsSource struct {
	once  sync.Once
	root  string
	value appPersonaDefaults
	err   error
}

type appPersonaWorkingReader func(path string) ([]byte, bool, error)

func (runtimeContext appRuntimeContext) readPersonaWorking(path string) ([]byte, bool, error) {
	if runtimeContext.personaReader == nil {
		return nil, false, errAppPersonaDefaultsInvalid
	}
	return runtimeContext.personaReader(path)
}

func (source *appPersonaDefaultsSource) load() (appPersonaDefaults, error) {
	if source == nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	source.once.Do(func() {
		source.value, source.err = loadAppPersonaDefaults(source.root)
	})
	if source.err != nil {
		return appPersonaDefaults{}, source.err
	}
	return source.value.clone(), nil
}

type appPersonaManifest struct {
	Version             int                       `json:"version"`
	Files               []appPersonaManifestEntry `json:"files"`
	TreeSHA256          string                    `json:"tree_sha256"`
	LegacyPersonaSHA256 []string                  `json:"legacy_persona_sha256"`
}

type appPersonaManifestEntry struct {
	Path   string `json:"path"`
	Bytes  uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type appPersonaIdentity struct {
	Version           int                       `json:"version"`
	DisplayName       string                    `json:"display_name"`
	PersonaIdentities []appPersonaIdentityEntry `json:"persona_identities"`
}

type appPersonaIdentityEntry struct {
	Role  string `json:"role"`
	Value string `json:"value"`
}

func loadAppPersonaDefaults(rawRoot string) (appPersonaDefaults, error) {
	root := rawRoot
	if root == "" || strings.TrimSpace(root) != root || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		!appPersonaPlatformPathSafe(root, true) || !appPersonaPathComponentsSafe(root) {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	defer rootHandle.Close()
	manifestBytes, err := readAppPersonaRootedFile(rootHandle, root, appPersonaManifestName, appPersonaMaxManifestBytes)
	if err != nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	manifest, err := parseAppPersonaManifest(manifestBytes)
	if err != nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	if err := validateAppPersonaManifestPaths(manifest.Files); err != nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}

	files := make(map[string][]byte, len(manifest.Files))
	tree := sha256.New()
	_, _ = tree.Write([]byte(appPersonaManifestTreeDomain))
	var treeBytes uint64
	for _, entry := range manifest.Files {
		limit := appPersonaMaxDefaultFileBytes
		if entry.Path == "persona.md" {
			limit = maxPersonaBytes
		}
		content, readErr := readAppPersonaRootedFile(rootHandle, root, entry.Path, limit)
		if readErr != nil || uint64(len(content)) != entry.Bytes || appPersonaSHA256(content) != entry.SHA256 {
			return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
		}
		treeBytes += uint64(len(content))
		if treeBytes > appPersonaMaxDefaultTreeBytes {
			return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
		}
		appPersonaWriteTreeField(tree, []byte(entry.Path))
		appPersonaWriteTreeField(tree, content)
		files[entry.Path] = bytes.Clone(content)
	}
	if hex.EncodeToString(tree.Sum(nil)) != manifest.TreeSHA256 {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	if err := validateAppPersonaDefaultTree(rootHandle, root, files); err != nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}

	identity, err := parseAppPersonaIdentity(files["identity.json"])
	if err != nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	if !appWorkingPersonaComplete(files["persona.md"]) {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	var personaContent strings.Builder
	for path, content := range files {
		if path == "identity.json" {
			continue
		}
		if !utf8.Valid(content) {
			return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
		}
		text := string(content)
		if !appPersonaTextBracesComplete(text) || personaValidationError(text) != "" {
			return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
		}
		personaContent.WriteString(text)
		personaContent.WriteByte('\n')
	}
	for _, declared := range identity.PersonaIdentities {
		if !strings.Contains(personaContent.String(), declared.Value) {
			return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
		}
	}
	legacy := make(map[string]struct{}, len(manifest.LegacyPersonaSHA256))
	for _, digest := range manifest.LegacyPersonaSHA256 {
		legacy[digest] = struct{}{}
	}
	return appPersonaDefaults{
		displayName: identity.DisplayName,
		persona:     bytes.Clone(files["persona.md"]), files: files, legacy: legacy,
	}, nil
}

func parseAppPersonaManifest(encoded []byte) (appPersonaManifest, error) {
	if len(encoded) == 0 || len(encoded) > appPersonaMaxManifestBytes || !utf8.Valid(encoded) ||
		encoded[len(encoded)-1] != '\n' || bytes.Contains(encoded, []byte{'\r'}) {
		return appPersonaManifest{}, errAppPersonaDefaultsInvalid
	}
	if err := validateAppPersonaJSONUnicodeScalars(encoded); err != nil {
		return appPersonaManifest{}, err
	}
	if err := validateAppPersonaJSONUnique(encoded); err != nil {
		return appPersonaManifest{}, err
	}
	var manifest appPersonaManifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return appPersonaManifest{}, err
	}
	if manifest.Version != 1 || len(manifest.Files) < 2 || len(manifest.Files) > appPersonaMaxDefaultFiles ||
		len(manifest.LegacyPersonaSHA256) > appPersonaMaxLegacyHashes || !appPersonaDigestValid(manifest.TreeSHA256) {
		return appPersonaManifest{}, errAppPersonaDefaultsInvalid
	}
	seenLegacy := make(map[string]struct{}, len(manifest.LegacyPersonaSHA256))
	for _, digest := range manifest.LegacyPersonaSHA256 {
		if !appPersonaDigestValid(digest) {
			return appPersonaManifest{}, errAppPersonaDefaultsInvalid
		}
		if _, duplicate := seenLegacy[digest]; duplicate {
			return appPersonaManifest{}, errAppPersonaDefaultsInvalid
		}
		seenLegacy[digest] = struct{}{}
	}
	canonical := canonicalAppPersonaManifest(manifest)
	if !bytes.Equal(encoded, canonical) {
		return appPersonaManifest{}, errAppPersonaDefaultsInvalid
	}
	return manifest, nil
}

func canonicalAppPersonaManifest(manifest appPersonaManifest) []byte {
	var result strings.Builder
	result.WriteString(`{"version":1,"files":[`)
	for index, entry := range manifest.Files {
		if index > 0 {
			result.WriteByte(',')
		}
		result.WriteString(`{"path":"`)
		result.WriteString(appPersonaJSONEncodedText(entry.Path))
		result.WriteString(`","bytes":`)
		result.WriteString(strconv.FormatUint(entry.Bytes, 10))
		result.WriteString(`,"sha256":"`)
		result.WriteString(entry.SHA256)
		result.WriteString(`"}`)
	}
	result.WriteString(`],"tree_sha256":"`)
	result.WriteString(manifest.TreeSHA256)
	result.WriteString(`","legacy_persona_sha256":[`)
	for index, digest := range manifest.LegacyPersonaSHA256 {
		if index > 0 {
			result.WriteByte(',')
		}
		result.WriteByte('"')
		result.WriteString(digest)
		result.WriteByte('"')
	}
	result.WriteString("]}\n")
	return []byte(result.String())
}

func appPersonaJSONEncodedText(value string) string {
	var result strings.Builder
	for _, unit := range utf16.Encode([]rune(value)) {
		if unit == '\\' {
			result.WriteString(`\\`)
			continue
		}
		if unit >= 0x20 && unit <= 0x7e && unit != '\\' && unit != '"' &&
			unit != '<' && unit != '>' && unit != '&' && unit != '\'' && unit != '+' && unit != '`' {
			result.WriteRune(rune(unit))
			continue
		}
		fmt.Fprintf(&result, `\u%04X`, unit)
	}
	return result.String()
}

func validateAppPersonaManifestPaths(entries []appPersonaManifestEntry) error {
	required := map[string]bool{"identity.json": false, "persona.md": false}
	paths := make([]string, 0, len(entries))
	for index, entry := range entries {
		if !appPersonaDefaultPathValid(entry.Path) || !appPersonaDigestValid(entry.SHA256) ||
			entry.Bytes > appPersonaMaxDefaultFileBytes ||
			(entry.Path == "persona.md" && entry.Bytes > maxPersonaBytes) {
			return errAppPersonaDefaultsInvalid
		}
		for _, prior := range paths {
			if appPersonaPlatformOrdinalIgnoreCaseEqual(prior, entry.Path) {
				return errAppPersonaDefaultsInvalid
			}
		}
		if index > 0 && appPersonaUTF16Compare(entries[index-1].Path, entry.Path) >= 0 {
			return errAppPersonaDefaultsInvalid
		}
		paths = append(paths, entry.Path)
		if _, ok := required[entry.Path]; ok {
			required[entry.Path] = true
		}
	}
	if !required["identity.json"] || !required["persona.md"] {
		return errAppPersonaDefaultsInvalid
	}
	return nil
}

func appPersonaDefaultPathValid(path string) bool {
	if path == "" || len([]byte(path)) > appPersonaMaxRelativePathBytes || !utf8.ValidString(path) ||
		!appPersonaPlatformNFC(path) || strings.Contains(path, "\\") || strings.ContainsAny(path, `<>:"|?*`) ||
		strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || path == appPersonaManifestName ||
		strings.HasSuffix(strings.ToLower(path), ".goc") {
		return false
	}
	for _, character := range path {
		if unicode.IsControl(character) {
			return false
		}
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part {
			return false
		}
	}
	switch path {
	case "identity.json", "persona.md", "roster.md":
		return true
	default:
		return strings.HasPrefix(path, "overlay/") && len(parts) > 1
	}
}

func appPersonaUTF16Compare(left, right string) int {
	a := utf16.Encode([]rune(left))
	b := utf16.Encode([]rune(right))
	for index := 0; index < len(a) && index < len(b); index++ {
		if a[index] < b[index] {
			return -1
		}
		if a[index] > b[index] {
			return 1
		}
	}
	return len(a) - len(b)
}

func parseAppPersonaIdentity(encoded []byte) (appPersonaIdentity, error) {
	if len(encoded) == 0 || len(encoded) > appPersonaMaxDefaultFileBytes || !utf8.Valid(encoded) ||
		validateAppPersonaJSONUnicodeScalars(encoded) != nil || validateAppPersonaJSONUnique(encoded) != nil ||
		validateAppPersonaIdentityKeys(encoded) != nil {
		return appPersonaIdentity{}, errAppPersonaDefaultsInvalid
	}
	var identity appPersonaIdentity
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil || identity.Version != 1 ||
		len(identity.PersonaIdentities) < 1 || len(identity.PersonaIdentities) > 8 {
		return appPersonaIdentity{}, errAppPersonaDefaultsInvalid
	}
	if !appPersonaIdentityValueValid(identity.DisplayName) {
		return appPersonaIdentity{}, errAppPersonaDefaultsInvalid
	}
	seen := make(map[string]struct{}, len(identity.PersonaIdentities))
	assistant := ""
	assistantCount := 0
	for _, entry := range identity.PersonaIdentities {
		if entry.Role != "assistant" && entry.Role != "expert" && entry.Role != "business" ||
			!appPersonaIdentityValueValid(entry.Value) {
			return appPersonaIdentity{}, errAppPersonaDefaultsInvalid
		}
		if _, duplicate := seen[entry.Value]; duplicate {
			return appPersonaIdentity{}, errAppPersonaDefaultsInvalid
		}
		seen[entry.Value] = struct{}{}
		if entry.Role == "assistant" {
			assistant = entry.Value
			assistantCount++
		}
	}
	if assistantCount != 1 || identity.DisplayName != assistant {
		return appPersonaIdentity{}, errAppPersonaDefaultsInvalid
	}
	return identity, nil
}

func validateAppPersonaIdentityKeys(encoded []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil || !appPersonaExactKeys(root,
		"version", "display_name", "persona_identities") {
		return errAppPersonaDefaultsInvalid
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(root["persona_identities"], &entries); err != nil {
		return errAppPersonaDefaultsInvalid
	}
	for _, entry := range entries {
		if !appPersonaExactKeys(entry, "role", "value") {
			return errAppPersonaDefaultsInvalid
		}
	}
	return nil
}

func appPersonaExactKeys(value map[string]json.RawMessage, keys ...string) bool {
	if len(value) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}

func appPersonaIdentityValueValid(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		len(value) > 256 || len([]rune(value)) > 60 || !appPersonaPlatformNFC(value) ||
		strings.ContainsAny(value, "\r\n") || strings.Contains(value, "{{") || strings.Contains(value, "}}") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateAppPersonaJSONUnique(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := validateAppPersonaJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return errAppPersonaDefaultsInvalid
	}
	return nil
}

func validateAppPersonaJSONUnicodeScalars(encoded []byte) error {
	inString := false
	for index := 0; index < len(encoded); index++ {
		character := encoded[index]
		if !inString {
			if character == '"' {
				inString = true
			}
			continue
		}
		if character == '"' {
			inString = false
			continue
		}
		if character != '\\' {
			continue
		}
		index++
		if index >= len(encoded) {
			return errAppPersonaDefaultsInvalid
		}
		if encoded[index] != 'u' {
			continue
		}
		if index+5 > len(encoded) {
			return errAppPersonaDefaultsInvalid
		}
		unit, valid := appPersonaJSONHexUnit(encoded[index+1 : index+5])
		if !valid {
			return errAppPersonaDefaultsInvalid
		}
		index += 4
		switch {
		case unit >= 0xd800 && unit <= 0xdbff:
			if index+7 > len(encoded) || encoded[index+1] != '\\' || encoded[index+2] != 'u' {
				return errAppPersonaDefaultsInvalid
			}
			low, valid := appPersonaJSONHexUnit(encoded[index+3 : index+7])
			if !valid || low < 0xdc00 || low > 0xdfff {
				return errAppPersonaDefaultsInvalid
			}
			index += 6
		case unit >= 0xdc00 && unit <= 0xdfff:
			return errAppPersonaDefaultsInvalid
		}
	}
	if inString {
		return errAppPersonaDefaultsInvalid
	}
	return nil
}

func appPersonaJSONHexUnit(encoded []byte) (uint16, bool) {
	if len(encoded) != 4 {
		return 0, false
	}
	var value uint16
	for _, character := range encoded {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func validateAppPersonaJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, valid := keyToken.(string)
			if keyErr != nil || !valid {
				return errAppPersonaDefaultsInvalid
			}
			if _, duplicate := seen[key]; duplicate {
				return errAppPersonaDefaultsInvalid
			}
			seen[key] = struct{}{}
			if err := validateAppPersonaJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errAppPersonaDefaultsInvalid
		}
	case '[':
		for decoder.More() {
			if err := validateAppPersonaJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errAppPersonaDefaultsInvalid
		}
	default:
		return errAppPersonaDefaultsInvalid
	}
	return nil
}

func validateAppPersonaDefaultTree(rootHandle *os.Root, root string, expected map[string][]byte) error {
	seen := make(map[string]struct{}, len(expected)+1)
	expectedDirectories := make(map[string]struct{})
	for relative := range expected {
		for directory := pathpkg.Dir(relative); directory != "."; directory = pathpkg.Dir(directory) {
			expectedDirectories[directory] = struct{}{}
		}
	}
	err := fs.WalkDir(rootHandle.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		platformPath := root
		if path != "." {
			platformPath = filepath.Join(root, filepath.FromSlash(path))
		}
		if walkErr != nil || !appPersonaPlatformPathSafe(platformPath, entry != nil && entry.IsDir()) {
			return errAppPersonaDefaultsInvalid
		}
		if path == "." {
			return nil
		}
		relative := filepath.ToSlash(path)
		if entry.IsDir() {
			if _, expected := expectedDirectories[relative]; !expected {
				return errAppPersonaDefaultsInvalid
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return errAppPersonaDefaultsInvalid
		}
		if relative != appPersonaManifestName {
			if _, ok := expected[relative]; !ok {
				return errAppPersonaDefaultsInvalid
			}
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil || len(seen) != len(expected)+1 {
		return errAppPersonaDefaultsInvalid
	}
	for path := range expected {
		if _, ok := seen[path]; !ok {
			return errAppPersonaDefaultsInvalid
		}
	}
	return nil
}

func readAppPersonaRootedFile(rootHandle *os.Root, root, relative string, maximum int) ([]byte, error) {
	if maximum < 0 || (relative != appPersonaManifestName && !appPersonaDefaultPathValid(relative)) {
		return nil, errAppPersonaDefaultsInvalid
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(path)+string(os.PathSeparator), cleanRoot) ||
		!appPersonaPathComponentsSafe(filepath.Dir(path)) || !appPersonaPlatformPathSafe(path, false) {
		return nil, errAppPersonaDefaultsInvalid
	}
	before, err := rootHandle.Lstat(filepath.FromSlash(relative))
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > int64(maximum) {
		return nil, errAppPersonaDefaultsInvalid
	}
	file, err := rootHandle.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, errAppPersonaDefaultsInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errAppPersonaDefaultsInvalid
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(content) > maximum {
		return nil, errAppPersonaDefaultsInvalid
	}
	after, err := rootHandle.Lstat(filepath.FromSlash(relative))
	if err != nil || !os.SameFile(opened, after) || !appPersonaPlatformPathSafe(path, false) {
		return nil, errAppPersonaDefaultsInvalid
	}
	return content, nil
}

func appPersonaPathComponentsSafe(path string) bool {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	current := volume + string(os.PathSeparator)
	for _, part := range strings.Split(strings.Trim(rest, `\/`), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		if !appPersonaPlatformPathSafe(current, true) {
			return false
		}
	}
	return true
}

func appPersonaDigestValid(digest string) bool {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

func appPersonaSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func appPersonaWriteTreeField(writer io.Writer, field []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(field)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write(field)
}

func (runtimeContext appRuntimeContext) completePackagedOnboardingPersona(
	ctx context.Context,
	expectedRevision int64,
) (store.OnboardingState, error) {
	defaults, err := runtimeContext.personaDefaults.load()
	if err != nil || runtimeContext.api == nil || runtimeContext.api.st == nil {
		return store.OnboardingState{}, errAppPersonaDefaultsInvalid
	}
	if ctx.Err() != nil {
		return store.OnboardingState{}, ctx.Err()
	}
	if runtimeContext.personaReader == nil {
		return store.OnboardingState{}, errAppPersonaRepairRequired
	}
	a := runtimeContext.api
	onboardingMutationMu.Lock()
	defer onboardingMutationMu.Unlock()
	if ctx.Err() != nil {
		return store.OnboardingState{}, ctx.Err()
	}
	snapshot, err := runtimeContext.onboardingStore().OnboardingSnapshot()
	if err != nil {
		return store.OnboardingState{}, err
	}
	if err := validateRuntimeOnboardingSnapshot(runtimeContext.registry, snapshot); err != nil {
		return store.OnboardingState{}, store.ErrOnboardingConfigurationChanged
	}
	state := snapshot.State
	if state.Revision != expectedRevision {
		return store.OnboardingState{}, store.ErrOnboardingConflict
	}
	if state.Phase != store.OnboardingPhasePersona || state.TestNonceHash != "" || state.TestExpiresAt != "" {
		return store.OnboardingState{}, store.ErrOnboardingInvalidPhase
	}
	personaPath, err := appPackagedPersonaWorkingPath(a)
	if err != nil {
		return store.OnboardingState{}, errAppPersonaRepairRequired
	}
	if err := a.resolveAgentPersonaRecovery(personaPath); err != nil {
		return store.OnboardingState{}, errAppPersonaRepairRequired
	}
	if err := validateExistingImmutablePersonaBackup(personaPath, defaults.persona); err != nil {
		return store.OnboardingState{}, errAppPersonaRepairRequired
	}
	working, missing, err := runtimeContext.readPersonaWorking(personaPath)
	if err != nil {
		return store.OnboardingState{}, errAppPersonaRepairRequired
	}
	seed := missing
	if !missing {
		_, seed = defaults.legacy[appPersonaSHA256(working)]
		if !seed && !appWorkingPersonaComplete(working) {
			return store.OnboardingState{}, errAppPersonaRepairRequired
		}
	}
	displayName := defaults.displayName
	storedName, err := appPackagedPersonaDisplayName(a.st)
	if err != nil {
		return store.OnboardingState{}, err
	}
	if appPersonaIdentityValueValid(storedName) {
		displayName = storedName
	}
	wasMissing := missing
	original := bytes.Clone(working)
	recoveryToken := ""
	if seed {
		if err := ensureImmutablePersonaBackup(personaPath, defaults.persona); err != nil {
			return store.OnboardingState{}, errAppPersonaRepairRequired
		}
		if !missing {
			recoveryToken, err = a.prepareAgentPersonaRecovery(personaPath, original, defaults.persona)
			if err != nil {
				return store.OnboardingState{}, errAppPersonaRepairRequired
			}
		}
		if err := writeAgentFileAtomic(personaPath, defaults.persona, 0o600); err != nil {
			if recoveryToken != "" {
				if cleanupErr := a.resolveAgentPersonaRecovery(personaPath); cleanupErr != nil {
					return store.OnboardingState{}, errAppPersonaRepairRequired
				}
			}
			return store.OnboardingState{}, errAppPersonaRepairRequired
		}
		working, missing, err = runtimeContext.readPersonaWorking(personaPath)
		if err != nil || missing || !bytes.Equal(working, defaults.persona) {
			if cleanupErr := compensateUncommittedPackagedPersonaPublication(
				a, personaPath, original, defaults.persona, wasMissing, recoveryToken,
			); cleanupErr != nil {
				return store.OnboardingState{}, errAppPersonaRepairRequired
			}
			return store.OnboardingState{}, errAppPersonaRepairRequired
		}
		if recoveryToken == "" {
			recoveryToken, err = a.prepareAgentPersonaRecovery(personaPath, working, working)
			if err != nil {
				if cleanupErr := compensateUncommittedPackagedPersonaPublication(
					a, personaPath, original, defaults.persona, wasMissing, recoveryToken,
				); cleanupErr != nil {
					return store.OnboardingState{}, errAppPersonaRepairRequired
				}
				return store.OnboardingState{}, errAppPersonaRepairRequired
			}
		}
	}
	if ctx.Err() != nil {
		if seed {
			if cleanupErr := compensateUncommittedPackagedPersonaPublication(
				a, personaPath, original, defaults.persona, wasMissing, recoveryToken,
			); cleanupErr != nil {
				return store.OnboardingState{}, errAppPersonaRepairRequired
			}
		}
		return store.OnboardingState{}, ctx.Err()
	}
	fingerprint := agentPersonaFingerprint(working, displayName)
	var updated store.OnboardingState
	if recoveryToken != "" {
		updated, err = a.st.AdvanceOnboardingPersonaWithRecovery(
			expectedRevision, fingerprint, displayName, recoveryToken,
		)
	} else {
		updated, err = a.st.AdvanceOnboardingPersona(expectedRevision, fingerprint, displayName)
	}
	if err != nil {
		if recoveryToken != "" {
			if cleanupErr := a.resolveAgentPersonaRecovery(personaPath); cleanupErr != nil {
				return store.OnboardingState{}, errAppPersonaRepairRequired
			}
		}
		return store.OnboardingState{}, err
	}
	if recoveryToken != "" {
		if err := a.resolveAgentPersonaRecovery(personaPath); err != nil {
			return store.OnboardingState{}, errAppPersonaRepairRequired
		}
	}
	return updated, nil
}

func compensateUncommittedPackagedPersonaPublication(
	a *api,
	personaPath string,
	original []byte,
	immutable []byte,
	wasMissing bool,
	recoveryToken string,
) error {
	restore := original
	if wasMissing {
		restore = immutable
	}
	if err := restoreAgentFileAtomic(personaPath, restore, 0o600); err != nil {
		return errAppPersonaRepairRequired
	}
	if recoveryToken != "" {
		if err := a.resolveAgentPersonaRecovery(personaPath); err != nil {
			return errAppPersonaRepairRequired
		}
	}
	return nil
}

func appPackagedPersonaWorkingPath(a *api) (string, error) {
	if a == nil || a.zalo == nil {
		return "", errAppPersonaDefaultsInvalid
	}
	path := strings.TrimSpace(a.zalo.cfg.PersonaPath)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		!appPersonaPathComponentsSafe(filepath.Dir(path)) {
		return "", errAppPersonaDefaultsInvalid
	}
	return path, nil
}

func readAppWorkingPersona(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() < 0 || info.Size() > maxPersonaBytes || !appPersonaPlatformPathSafe(path, false) {
		return nil, false, errAppPersonaDefaultsInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, errAppPersonaDefaultsInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, false, errAppPersonaDefaultsInvalid
	}
	content, err := io.ReadAll(io.LimitReader(file, maxPersonaBytes+1))
	if err != nil || len(content) > maxPersonaBytes {
		return nil, false, errAppPersonaDefaultsInvalid
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || !appPersonaPlatformPathSafe(path, false) {
		return nil, false, errAppPersonaDefaultsInvalid
	}
	return content, false, nil
}

func appWorkingPersonaComplete(content []byte) bool {
	if len(content) == 0 || len(content) > maxPersonaBytes || !utf8.Valid(content) || strings.TrimSpace(string(content)) == "" {
		return false
	}
	text := string(content)
	analysis := analyzePersona(text)
	if len(analysis.Placeholders) != 0 || analysis.ValidationError != "" {
		return false
	}
	return appPersonaTextBracesComplete(text)
}

func appPersonaTextBracesComplete(text string) bool {
	if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
		return false
	}
	depth := 0
	for _, character := range text {
		switch character {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func validateExistingImmutablePersonaBackup(path string, immutable []byte) error {
	backupPath := path + ".goc"
	content, missing, err := readAppWorkingPersona(backupPath)
	if err != nil {
		return err
	}
	if missing {
		return nil
	}
	if !bytes.Equal(content, immutable) {
		return errAppPersonaDefaultsInvalid
	}
	return nil
}

func ensureImmutablePersonaBackup(path string, immutable []byte) error {
	if err := validateExistingImmutablePersonaBackup(path, immutable); err != nil {
		return err
	}
	if _, err := os.Lstat(path + ".goc"); errors.Is(err, os.ErrNotExist) {
		if err := writeAppBackupOnce(path+".goc", immutable, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	content, missing, err := readAppWorkingPersona(path + ".goc")
	if err != nil || missing || !bytes.Equal(content, immutable) {
		return errAppPersonaDefaultsInvalid
	}
	return nil
}

func (runtimeContext appRuntimeContext) loadPersonaMutationDefaults() (appPersonaDefaults, error) {
	defaults, err := runtimeContext.personaDefaults.load()
	if err != nil {
		return appPersonaDefaults{}, errAppPersonaDefaultsInvalid
	}
	return defaults, nil
}

func prepareImmutablePersonaBackup(path string, defaults appPersonaDefaults) error {
	if _, missing, err := readAppWorkingPersona(path); err != nil || missing {
		return errAppPersonaDefaultsInvalid
	}
	return ensureImmutablePersonaBackup(path, defaults.persona)
}
