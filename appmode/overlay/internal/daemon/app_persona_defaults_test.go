package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"agentdc/internal/store"
)

const (
	packagedPersonaTestName = "Bot Mặc Định"
	packagedPersonaTestText = "Tôi là Bot Mặc Định. Trả lời rõ ràng.\n"
)

type packagedPersonaFixture struct {
	root       string
	persona    []byte
	legacy     []byte
	manifest   []byte
	identity   []byte
	defaultMap map[string][]byte
}

func newPackagedPersonaFixture(t *testing.T) packagedPersonaFixture {
	t.Helper()
	root := t.TempDir()
	identity := []byte(`{"version":1,"display_name":"Bot Mặc Định","persona_identities":[{"role":"assistant","value":"Bot Mặc Định"}]}`)
	persona := []byte(packagedPersonaTestText)
	legacy := []byte("Tôi là {{TEN_BOT}}.\n")
	files := map[string][]byte{
		"identity.json": identity,
		"persona.md":    persona,
	}
	paths := []string{"identity.json", "persona.md"}
	legacySum := sha256.Sum256(legacy)
	manifest := packagedPersonaManifest(t, paths, files, []string{hex.EncodeToString(legacySum[:])})
	for path, content := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "build-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	return packagedPersonaFixture{
		root: root, persona: persona, legacy: legacy, manifest: manifest,
		identity: identity, defaultMap: files,
	}
}

func packagedPersonaManifest(
	t *testing.T,
	paths []string,
	files map[string][]byte,
	legacy []string,
) []byte {
	t.Helper()
	h := sha256.New()
	_, _ = h.Write([]byte("agentdc/persona-default-tree/v1\x00"))
	entries := make([]string, 0, len(paths))
	for _, path := range paths {
		content := files[path]
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(path)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(path))
		binary.BigEndian.PutUint64(size[:], uint64(len(content)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(content)
		digest := sha256.Sum256(content)
		entries = append(entries, fmt.Sprintf(
			`{"path":"%s","bytes":%d,"sha256":"%x"}`,
			packagedPersonaJSONPath(path), len(content), digest,
		))
	}
	quotedLegacy := make([]string, len(legacy))
	for index, digest := range legacy {
		quotedLegacy[index] = `"` + digest + `"`
	}
	return []byte(fmt.Sprintf(
		`{"version":1,"files":[%s],"tree_sha256":"%x","legacy_persona_sha256":[%s]}`+"\n",
		strings.Join(entries, ","), h.Sum(nil), strings.Join(quotedLegacy, ","),
	))
}

func packagedPersonaJSONPath(path string) string {
	var encoded strings.Builder
	for _, unit := range utf16.Encode([]rune(path)) {
		if unit == '\\' {
			encoded.WriteString(`\\`)
			continue
		}
		if unit >= 0x20 && unit <= 0x7e && unit != '\\' && unit != '"' &&
			unit != '<' && unit != '>' && unit != '&' && unit != '\'' && unit != '+' && unit != '`' {
			encoded.WriteRune(rune(unit))
		} else {
			fmt.Fprintf(&encoded, `\u%04X`, unit)
		}
	}
	return encoded.String()
}

func TestPersonaDefaultsJSONEncodedTextMatchesDotNetPrintableBasicLatin(t *testing.T) {
	const input = " !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"
	const want = ` !\u0022#$%\u0026\u0027()*\u002B,-./0123456789:;\u003C=\u003E?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_\u0060abcdefghijklmnopqrstuvwxyz{|}~`
	if got := appPersonaJSONEncodedText(input); got != want {
		t.Fatalf("JsonEncodedText Basic Latin = %q; want %q", got, want)
	}
}

func TestPersonaDefaultsLoadStrictManifestAndCloneImmutableBytes(t *testing.T) {
	fixture := newPackagedPersonaFixture(t)
	source := &appPersonaDefaultsSource{root: fixture.root}
	first, err := source.load()
	if err != nil {
		t.Fatalf("load() = %v", err)
	}
	if first.displayName != packagedPersonaTestName || !bytes.Equal(first.persona, fixture.persona) {
		t.Fatalf("defaults = name %q persona %q", first.displayName, first.persona)
	}
	first.persona[0] ^= 0xff
	if err := os.WriteFile(filepath.Join(fixture.root, "persona.md"), []byte("disk drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), []byte("manifest drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := source.load()
	if err != nil {
		t.Fatalf("second load() = %v", err)
	}
	if !bytes.Equal(second.persona, fixture.persona) {
		t.Fatalf("load exposed mutable cached bytes: %q", second.persona)
	}
}

func TestPersonaDefaultsRejectManifestAndTreeDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, packagedPersonaFixture)
	}{
		{"duplicate manifest key", func(t *testing.T, f packagedPersonaFixture) {
			bad := bytes.Replace(f.manifest, []byte(`{"version":1`), []byte(`{"version":1,"version":1`), 1)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"unknown manifest key", func(t *testing.T, f packagedPersonaFixture) {
			bad := bytes.Replace(f.manifest, []byte(`{"version":1`), []byte(`{"unknown":0,"version":1`), 1)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"case folded manifest key", func(t *testing.T, f packagedPersonaFixture) {
			bad := bytes.Replace(f.manifest, []byte(`"tree_sha256"`), []byte(`"Tree_sha256"`), 1)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"noncanonical whitespace", func(t *testing.T, f packagedPersonaFixture) {
			bad := bytes.Replace(f.manifest, []byte(`"files":`), []byte(`"files": `), 1)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"duplicate nested key", func(t *testing.T, f packagedPersonaFixture) {
			needle := []byte(fmt.Sprintf(`"bytes":%d`, len(f.identity)))
			bad := bytes.Replace(f.manifest, needle, append(append([]byte{}, needle...), append([]byte(","), needle...)...), 1)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"uppercase hash", func(t *testing.T, f packagedPersonaFixture) {
			digest := sha256.Sum256(f.persona)
			lower := []byte(hex.EncodeToString(digest[:]))
			bad := bytes.Replace(f.manifest, lower, bytes.ToUpper(lower), 1)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"declared bytes drift", func(t *testing.T, f packagedPersonaFixture) {
			bad := bytes.Replace(f.manifest,
				[]byte(fmt.Sprintf(`"bytes":%d`, len(f.persona))),
				[]byte(fmt.Sprintf(`"bytes":%d`, len(f.persona)+1)), 1)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"tree digest drift", func(t *testing.T, f packagedPersonaFixture) {
			marker := []byte(`"tree_sha256":"`)
			index := bytes.Index(f.manifest, marker) + len(marker)
			bad := bytes.Clone(f.manifest)
			bad[index] = 'f'
			if f.manifest[index] == 'f' {
				bad[index] = 'e'
			}
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"too many legacy hashes", func(t *testing.T, f packagedPersonaFixture) {
			legacy := make([]string, 9)
			for index := range legacy {
				legacy[index] = fmt.Sprintf("%064x", index+1)
			}
			bad := packagedPersonaManifest(t, []string{"identity.json", "persona.md"}, f.defaultMap, legacy)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"unsorted paths", func(t *testing.T, f packagedPersonaFixture) {
			bad := packagedPersonaManifest(t, []string{"persona.md", "identity.json"}, f.defaultMap, nil)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"duplicate path", func(t *testing.T, f packagedPersonaFixture) {
			bad := packagedPersonaManifest(t, []string{"identity.json", "persona.md", "persona.md"}, f.defaultMap, nil)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"case folded path", func(t *testing.T, f packagedPersonaFixture) {
			files := map[string][]byte{"identity.json": f.identity, "persona.md": f.persona, "PERSONA.md": f.persona}
			bad := packagedPersonaManifest(t, []string{"identity.json", "PERSONA.md", "persona.md"}, files, nil)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"invalid relative path", func(t *testing.T, f packagedPersonaFixture) {
			escapeName := "persona-default-escape.md"
			escape := []byte("Bot Mặc Định escaped\n")
			if err := os.WriteFile(filepath.Join(filepath.Dir(f.root), escapeName), escape, 0o600); err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{"identity.json": f.identity, "persona.md": f.persona, "../" + escapeName: escape}
			bad := packagedPersonaManifest(t, []string{"../" + escapeName, "identity.json", "persona.md"}, files, nil)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), bad, 0o600)
		}},
		{"file bytes drift", func(t *testing.T, f packagedPersonaFixture) {
			os.WriteFile(filepath.Join(f.root, "persona.md"), append(f.persona, '!'), 0o600)
		}},
		{"extra file", func(t *testing.T, f packagedPersonaFixture) {
			os.WriteFile(filepath.Join(f.root, "extra.md"), []byte("extra"), 0o600)
		}},
		{"backup forbidden", func(t *testing.T, f packagedPersonaFixture) {
			os.WriteFile(filepath.Join(f.root, "persona.md.goc"), f.persona, 0o600)
		}},
		{"non NFC path", func(t *testing.T, f packagedPersonaFixture) {
			path := "overlay/Cafe\u0301.md"
			files := map[string][]byte{
				"identity.json": f.identity, "persona.md": f.persona, path: []byte("Bot Mặc Định\n"),
			}
			if err := os.MkdirAll(filepath.Join(f.root, "overlay"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.root, filepath.FromSlash(path)), files[path], 0o600); err != nil {
				t.Fatal(err)
			}
			manifest := packagedPersonaManifest(t, []string{"identity.json", path, "persona.md"}, files, nil)
			os.WriteFile(filepath.Join(f.root, "build-manifest.json"), manifest, 0o600)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPackagedPersonaFixture(t)
			tt.mutate(t, fixture)
			if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
				t.Fatal("load() error = nil")
			}
		})
	}
}

func TestPersonaDefaultsRejectControlCharacterPathPolicy(t *testing.T) {
	if appPersonaDefaultPathValid("overlay/a\tb.md") {
		t.Fatal("control-character path passed loader policy")
	}
}

func TestPersonaDefaultsRejectUTF16OrdinalCounterexampleAndUnsafeRoots(t *testing.T) {
	t.Run("UTF-16 ordinal order", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		astral := "overlay/\U00010000.md"
		bmp := "overlay/\ue000.md"
		files := map[string][]byte{
			"identity.json": fixture.identity, "persona.md": fixture.persona,
			astral: []byte("Bot Mặc Định astral\n"), bmp: []byte("Bot Mặc Định bmp\n"),
		}
		for _, path := range []string{astral, bmp} {
			full := filepath.Join(fixture.root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, files[path], 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Go code-point order puts U+E000 first. .NET ordinal UTF-16 puts the
		// U+D800 surrogate for U+10000 first, so this manifest must fail.
		bad := packagedPersonaManifest(t,
			[]string{"identity.json", bmp, astral, "persona.md"}, files, nil)
		if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
			t.Fatal("Go/code-point sorted paths were accepted as .NET ordinal")
		}
	})
	t.Run("valid UTF-16 ordinal optional tree", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		astral := "overlay/\U00010000.md"
		bmp := "overlay/\ue000.md"
		files := map[string][]byte{
			"identity.json": fixture.identity, "persona.md": fixture.persona,
			astral: []byte("Bot Mặc Định astral\n"), bmp: []byte("Bot Mặc Định bmp\n"),
			"roster.md": []byte("Bot Mặc Định roster\n"),
		}
		paths := []string{"identity.json", astral, bmp, "persona.md", "roster.md"}
		for _, path := range paths {
			if path == "identity.json" || path == "persona.md" {
				continue
			}
			full := filepath.Join(fixture.root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, files[path], 0o600); err != nil {
				t.Fatal(err)
			}
		}
		manifest := packagedPersonaManifest(t, paths, files, nil)
		if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err != nil {
			t.Fatalf("valid optional UTF-16 ordinal tree rejected: %v", err)
		}
	})
	t.Run("valid .NET ordinal ignore-case counterexample", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		ascii := "overlay/s.md"
		longS := "overlay/\u017f.md"
		files := map[string][]byte{
			"identity.json": fixture.identity, "persona.md": fixture.persona,
			ascii: []byte("Bot Mặc Định ascii\n"), longS: []byte("Bot Mặc Định long-s\n"),
		}
		paths := []string{"identity.json", ascii, longS, "persona.md"}
		for _, path := range []string{ascii, longS} {
			full := filepath.Join(fixture.root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, files[path], 0o600); err != nil {
				t.Fatal(err)
			}
		}
		manifest := packagedPersonaManifest(t, paths, files, nil)
		if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err != nil {
			t.Fatalf("Task9-valid OrdinalIgnoreCase paths rejected: %v", err)
		}
	})
	t.Run("valid JsonEncodedText plus and backtick path", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		optional := "overlay/a+`b.md"
		files := map[string][]byte{
			"identity.json": fixture.identity, "persona.md": fixture.persona,
			optional: []byte("Bot Mặc Định punctuation\n"),
		}
		full := filepath.Join(fixture.root, filepath.FromSlash(optional))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, files[optional], 0o600); err != nil {
			t.Fatal(err)
		}
		manifest := packagedPersonaManifest(
			t, []string{"identity.json", optional, "persona.md"}, files, nil,
		)
		if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err != nil {
			t.Fatalf("Task9-valid JsonEncodedText path rejected: %v", err)
		}
	})
	for _, root := range []string{"", ".", "relative/defaults"} {
		if _, err := (&appPersonaDefaultsSource{root: root}).load(); err == nil {
			t.Fatalf("unsafe root %q accepted", root)
		}
	}
	t.Run("surrounding root whitespace", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		if _, err := (&appPersonaDefaultsSource{root: " " + fixture.root + " "}).load(); err == nil {
			t.Fatal("loader normalized rather than rejected the captured root")
		}
	})
	t.Run("symlinked root", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		link := filepath.Join(t.TempDir(), "linked-defaults")
		makeOnboardingTestDirLink(t, fixture.root, link)
		if _, err := (&appPersonaDefaultsSource{root: link}).load(); err == nil {
			t.Fatal("symlinked root accepted")
		}
	})
	t.Run("reparse child", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "note.md"), []byte("Bot Mặc Định\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(fixture.root, "overlay")
		makeOnboardingTestDirLink(t, outside, link)
		files := map[string][]byte{
			"identity.json": fixture.identity, "persona.md": fixture.persona,
			"overlay/note.md": []byte("Bot Mặc Định\n"),
		}
		manifest := packagedPersonaManifest(t,
			[]string{"identity.json", "overlay/note.md", "persona.md"}, files, nil)
		if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
			t.Fatal("reparse child accepted")
		}
	})
	t.Run("nonregular default", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, "persona.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(fixture.root, "persona.md"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
			t.Fatal("directory default accepted")
		}
	})
	t.Run("unmanifested empty directory", func(t *testing.T) {
		fixture := newPackagedPersonaFixture(t)
		if err := os.MkdirAll(filepath.Join(fixture.root, "overlay", "unused"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
			t.Fatal("unmanifested empty directory accepted")
		}
	})
}

func TestPersonaDefaultsContextCapturesExactRootAndCopiesOnlySourcePointer(t *testing.T) {
	fixture := newPackagedPersonaFixture(t)
	other := t.TempDir()
	t.Setenv("AGENTDC_ZALO_PERSONA_DEFAULT_DIR", fixture.root)
	ctx := productionAppRuntimeContext(&api{})
	t.Setenv("AGENTDC_ZALO_PERSONA_DEFAULT_DIR", other)
	if ctx.personaDefaults == nil || ctx.personaDefaults.root != fixture.root {
		t.Fatalf("captured defaults = %#v; want exact initial environment root", ctx.personaDefaults)
	}
	copyOfContext := ctx
	if copyOfContext.personaDefaults != ctx.personaDefaults {
		t.Fatal("appRuntimeContext copied a sync.Once-bearing source value")
	}
	if _, err := copyOfContext.personaDefaults.load(); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.personaDefaults.load(); err != nil {
		t.Fatalf("shared once-backed source failed through original context: %v", err)
	}
}

func TestPersonaDefaultsRejectIdentityMismatchAndWritableProseIdentity(t *testing.T) {
	fixture := newPackagedPersonaFixture(t)
	identity := []byte(`{"version":1,"display_name":"Giả Mạo","persona_identities":[{"role":"assistant","value":"Giả Mạo"}]}`)
	files := map[string][]byte{"identity.json": identity, "persona.md": fixture.persona}
	manifest := packagedPersonaManifest(t, []string{"identity.json", "persona.md"}, files, nil)
	if err := os.WriteFile(filepath.Join(fixture.root, "identity.json"), identity, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
		t.Fatal("identity absent from immutable Persona content was accepted")
	}
}

func TestPersonaDefaultsRejectUnmatchedSingleBraceInImmutableContent(t *testing.T) {
	for _, relative := range []string{"persona.md", "roster.md", "overlay/note.md"} {
		t.Run(relative, func(t *testing.T) {
			fixture := newPackagedPersonaFixture(t)
			files := map[string][]byte{
				"identity.json": fixture.identity,
				"persona.md":    fixture.persona,
			}
			files[relative] = []byte("Bot Mặc Định {oops\n")
			paths := []string{"identity.json"}
			if relative == "overlay/note.md" {
				paths = append(paths, relative, "persona.md")
			} else {
				paths = append(paths, "persona.md")
				if relative == "roster.md" {
					paths = append(paths, relative)
				}
			}
			full := filepath.Join(fixture.root, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, files[relative], 0o600); err != nil {
				t.Fatal(err)
			}
			manifest := packagedPersonaManifest(t, paths, files, nil)
			if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
				t.Fatal("immutable unmatched single brace accepted")
			}
		})
	}
}

func TestPersonaDefaultsRejectStrictIdentitySchemaMatrix(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		persona  string
	}{
		{"duplicate root key", `{"version":1,"version":1,"display_name":"Bot Mặc Định","persona_identities":[{"role":"assistant","value":"Bot Mặc Định"}]}`, "Bot Mặc Định\n"},
		{"unknown nested key", `{"version":1,"display_name":"Bot Mặc Định","persona_identities":[{"role":"assistant","value":"Bot Mặc Định","extra":true}]}`, "Bot Mặc Định\n"},
		{"case folded nested key", `{"version":1,"display_name":"Bot Mặc Định","persona_identities":[{"Role":"assistant","value":"Bot Mặc Định"}]}`, "Bot Mặc Định\n"},
		{"two assistants", `{"version":1,"display_name":"Bot Mặc Định","persona_identities":[{"role":"assistant","value":"Bot Mặc Định"},{"role":"assistant","value":"Bot Khác"}]}`, "Bot Mặc Định Bot Khác\n"},
		{"invalid role", `{"version":1,"display_name":"Bot Mặc Định","persona_identities":[{"role":"operator","value":"Bot Mặc Định"}]}`, "Bot Mặc Định\n"},
		{"display mismatch", `{"version":1,"display_name":"Bot Khác","persona_identities":[{"role":"assistant","value":"Bot Mặc Định"}]}`, "Bot Mặc Định Bot Khác\n"},
		{"non NFC", `{"version":1,"display_name":"Cafe\u0301","persona_identities":[{"role":"assistant","value":"Cafe\u0301"}]}`, "Cafe\u0301\n"},
		{"embedded control", `{"version":1,"display_name":"Bot\u0009Name","persona_identities":[{"role":"assistant","value":"Bot\u0009Name"}]}`, "Bot\tName\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPackagedPersonaFixture(t)
			identity := []byte(tt.identity)
			persona := []byte(tt.persona)
			files := map[string][]byte{"identity.json": identity, "persona.md": persona}
			if err := os.WriteFile(filepath.Join(fixture.root, "identity.json"), identity, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.root, "persona.md"), persona, 0o600); err != nil {
				t.Fatal(err)
			}
			manifest := packagedPersonaManifest(t, []string{"identity.json", "persona.md"}, files, nil)
			if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
				t.Fatal("invalid identity schema accepted")
			}
		})
	}
}

func TestPersonaDefaultsRejectUnpairedSurrogateIdentityEscape(t *testing.T) {
	fixture := newPackagedPersonaFixture(t)
	identity := []byte(`{"version":1,"display_name":"\uD800","persona_identities":[{"role":"assistant","value":"\uD800"}]}`)
	persona := []byte("\uFFFD\n")
	files := map[string][]byte{"identity.json": identity, "persona.md": persona}
	manifest := packagedPersonaManifest(t, []string{"identity.json", "persona.md"}, files, nil)
	if err := os.WriteFile(filepath.Join(fixture.root, "identity.json"), identity, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "persona.md"), persona, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
		t.Fatal("identity JSON with an unpaired surrogate escape was accepted")
	}
}

func TestPersonaDefaultsAcceptPairedSurrogateIdentityEscape(t *testing.T) {
	fixture := newPackagedPersonaFixture(t)
	identity := []byte(`{"version":1,"display_name":"Bot \uD83D\uDE00","persona_identities":[{"role":"assistant","value":"Bot \uD83D\uDE00"}]}`)
	persona := []byte("Tôi là Bot 😀.\n")
	files := map[string][]byte{"identity.json": identity, "persona.md": persona}
	manifest := packagedPersonaManifest(t, []string{"identity.json", "persona.md"}, files, nil)
	if err := os.WriteFile(filepath.Join(fixture.root, "identity.json"), identity, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "persona.md"), persona, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err != nil {
		t.Fatalf("identity JSON with a paired surrogate escape was rejected: %v", err)
	}
}

func TestPersonaDefaultsRejectBlankPersonaWhenIdentityExistsOnlyInRoster(t *testing.T) {
	fixture := newPackagedPersonaFixture(t)
	persona := []byte(" \t\n")
	roster := []byte("Bot Mặc Định roster\n")
	files := map[string][]byte{
		"identity.json": fixture.identity,
		"persona.md":    persona,
		"roster.md":     roster,
	}
	manifest := packagedPersonaManifest(
		t, []string{"identity.json", "persona.md", "roster.md"}, files, nil,
	)
	for relative, content := range files {
		if err := os.WriteFile(filepath.Join(fixture.root, relative), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&appPersonaDefaultsSource{root: fixture.root}).load(); err == nil {
		t.Fatal("blank immutable persona.md was accepted because roster contained the identity")
	}
}

func TestPackagedPersonaSeedsMissingAndMigratesOnlyApprovedLegacy(t *testing.T) {
	for _, tt := range []struct {
		name     string
		existing []byte
	}{
		{"missing", nil},
		{"approved legacy", []byte("Tôi là {{TEN_BOT}}.\n")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, tt.existing, 101)
			state, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision)
			if err != nil {
				t.Fatalf("completePackagedOnboardingPersona() = %v", err)
			}
			if state.Phase != store.OnboardingPhaseTest || state.Revision != revision+1 {
				t.Fatalf("state = %+v; want Test r+1", state)
			}
			personaPath := harness.env.a.zalo.cfg.PersonaPath
			got, err := os.ReadFile(personaPath)
			if err != nil || !bytes.Equal(got, fixture.persona) {
				t.Fatalf("working Persona = %q, %v", got, err)
			}
			backup, err := os.ReadFile(personaPath + ".goc")
			if err != nil || !bytes.Equal(backup, fixture.persona) {
				t.Fatalf("immutable backup = %q, %v", backup, err)
			}
		})
	}
}

func TestPackagedPersonaDisplayNameReadFailurePrecedesLegacyPublication(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 109)
	path := harness.env.a.zalo.cfg.PersonaPath
	beforeState := onboardingBootstrapState(t, harness)
	var probes atomic.Int32
	setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
		probes.Add(1)
		return "Xin chào, tôi là Bot Mặc Định.", nil
	})
	originalDisplayName := appPackagedPersonaDisplayName
	appPackagedPersonaDisplayName = func(*store.Store) (string, error) {
		return "", errors.New("PRIVATE_DISPLAY_NAME_READ_FAILURE")
	}
	t.Cleanup(func() { appPackagedPersonaDisplayName = originalDisplayName })
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), revision,
	)
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
	if probes.Load() != 0 {
		t.Fatalf("display-name failure executed %d probes", probes.Load())
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.legacy) {
		t.Fatalf("display-name failure changed legacy Persona: %q, %v", got, err)
	}
	for _, sideEffect := range []string{path + ".goc", agentPersonaRecoveryPath(path)} {
		if _, err := os.Lstat(sideEffect); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("display-name failure published %q: %v", sideEffect, err)
		}
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("display-name failure changed Store: before=%+v after=%+v", beforeState, after)
	}
	for _, private := range []string{
		"PRIVATE_DISPLAY_NAME_READ_FAILURE", path,
		string(fixture.legacy), string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("display-name failure leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestPackagedPersonaLegacyCancellationAfterWriteRestoresBeforeReturn(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 110)
	path := harness.env.a.zalo.cfg.PersonaPath
	beforeState := onboardingBootstrapState(t, harness)
	requestContext, cancel := context.WithCancel(context.Background())
	originalWriter := writeAgentFileAtomic
	var writes atomic.Int32
	writeAgentFileAtomic = func(target string, content []byte, mode os.FileMode) error {
		err := originalWriter(target, content, mode)
		if target == path && err == nil {
			writes.Add(1)
			cancel()
		}
		return err
	}
	t.Cleanup(func() { writeAgentFileAtomic = originalWriter })
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), requestContext, revision,
	)
	requireOnboardingCode(t, rr, http.StatusRequestTimeout, "ONBOARDING_REQUEST_CANCELED")
	if writes.Load() != 1 {
		t.Fatalf("working Persona writes = %d; want cancel after one publication", writes.Load())
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.legacy) {
		t.Fatalf("cancellation did not restore legacy Persona: %q, %v", got, err)
	}
	backup, err := os.ReadFile(path + ".goc")
	if err != nil || !bytes.Equal(backup, fixture.persona) {
		t.Fatalf("cancellation lost immutable backup: %q, %v", backup, err)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancellation left recovery pending: %v", err)
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("cancellation changed Store: before=%+v after=%+v", beforeState, after)
	}
	for _, private := range []string{path, string(fixture.legacy), string(fixture.persona), packagedPersonaTestName} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("cancellation leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestPackagedPersonaCancellationRollbackFailureReturnsRepairConflict(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 11001)
	path := harness.env.a.zalo.cfg.PersonaPath
	beforeState := onboardingBootstrapState(t, harness)
	requestContext, cancel := context.WithCancel(context.Background())
	originalWriter := writeAgentFileAtomic
	writeAgentFileAtomic = func(target string, content []byte, mode os.FileMode) error {
		err := originalWriter(target, content, mode)
		if target == path && err == nil {
			cancel()
		}
		return err
	}
	originalRestore := restoreAgentFileAtomic
	restoreAgentFileAtomic = func(string, []byte, os.FileMode) error {
		return errors.New("PRIVATE_CANCEL_RESTORE_FAILURE")
	}
	t.Cleanup(func() {
		writeAgentFileAtomic = originalWriter
		restoreAgentFileAtomic = originalRestore
	})
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), requestContext, revision,
	)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.persona) {
		t.Fatalf("failed cancellation rollback Persona = %q, %v; want replacement retained", got, err)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(path)); err != nil {
		t.Fatalf("failed cancellation rollback lost recovery obligation: %v", err)
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("failed cancellation rollback changed Store: before=%+v after=%+v", beforeState, after)
	}
	for _, private := range []string{
		"PRIVATE_CANCEL_RESTORE_FAILURE", path, string(fixture.legacy),
		string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("failed cancellation rollback leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestPackagedPersonaLegacyRereadFailureRestoresBeforeReturn(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 1101)
	path := harness.env.a.zalo.cfg.PersonaPath
	beforeState := onboardingBootstrapState(t, harness)
	originalWriter := writeAgentFileAtomic
	writeAgentFileAtomic = func(target string, content []byte, mode os.FileMode) error {
		if err := originalWriter(target, content, mode); err != nil {
			return err
		}
		if target == path {
			return os.WriteFile(target, []byte("PRIVATE_CORRUPT_AFTER_WRITE"), mode)
		}
		return nil
	}
	t.Cleanup(func() { writeAgentFileAtomic = originalWriter })
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), revision,
	)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.legacy) {
		t.Fatalf("re-read failure did not restore legacy Persona: %q, %v", got, err)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("re-read failure left recovery pending: %v", err)
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("re-read failure changed Store: before=%+v after=%+v", beforeState, after)
	}
	for _, private := range []string{
		"PRIVATE_CORRUPT_AFTER_WRITE", path, string(fixture.legacy),
		string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("re-read failure leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestPackagedPersonaRollbackFailureReturnsRepairConflict(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 1102)
	path := harness.env.a.zalo.cfg.PersonaPath
	beforeState := onboardingBootstrapState(t, harness)
	if _, err := harness.env.db.Exec(`CREATE TRIGGER fail_packaged_persona_advance
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'PRIVATE_STORE_ADVANCE_FAILURE'); END`); err != nil {
		t.Fatal(err)
	}
	originalRestore := restoreAgentFileAtomic
	restoreAgentFileAtomic = func(string, []byte, os.FileMode) error {
		return errors.New("PRIVATE_RESTORE_FAILURE")
	}
	t.Cleanup(func() { restoreAgentFileAtomic = originalRestore })
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), revision,
	)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.persona) {
		t.Fatalf("failed rollback working Persona = %q, %v; want published replacement retained", got, err)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(path)); err != nil {
		t.Fatalf("failed rollback lost recovery obligation: %v", err)
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("failed rollback changed Store: before=%+v after=%+v", beforeState, after)
	}
	for _, private := range []string{
		"PRIVATE_STORE_ADVANCE_FAILURE", "PRIVATE_RESTORE_FAILURE", path,
		string(fixture.legacy), string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("failed rollback leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestPackagedPersonaPreservesCompleteUserEditAndCommittedName(t *testing.T) {
	userPersona := []byte("Tôi là Tên Người Dùng. Nội dung đã sửa hoàn chỉnh.\n")
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, userPersona, 111)
	if err := os.WriteFile(harness.env.a.zalo.cfg.PersonaPath+".goc", fixture.persona, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := harness.env.a.st.SetAgentDisplayName("Tên Người Dùng"); err != nil {
		t.Fatal(err)
	}
	state, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != revision+1 || state.Phase != store.OnboardingPhaseTest {
		t.Fatalf("state = %+v", state)
	}
	if state.PersonaFingerprint != agentPersonaFingerprint(userPersona, "Tên Người Dùng") {
		t.Fatalf("fingerprint = %q", state.PersonaFingerprint)
	}
	got, err := os.ReadFile(harness.env.a.zalo.cfg.PersonaPath)
	if err != nil || !bytes.Equal(got, userPersona) {
		t.Fatalf("user Persona changed: %q, %v", got, err)
	}
	if name, err := harness.env.a.st.AgentDisplayName(); err != nil || name != "Tên Người Dùng" {
		t.Fatalf("display name = %q, %v", name, err)
	}
}

func TestPackagedPersonaPreservesCompleteWorkingCopyWithoutEagerBackup(t *testing.T) {
	tests := []struct {
		name       string
		working    []byte
		storedName string
		wantName   string
	}{
		{
			name: "immutable working copy", working: []byte("Tôi là Bot Mặc Định. Trả lời rõ ràng.\n"),
			wantName: packagedPersonaTestName,
		},
		{
			name: "user edited working copy", working: []byte("Tôi là Tên Người Dùng. Bản sửa hoàn chỉnh.\n"),
			storedName: "Tên Người Dùng", wantName: "Tên Người Dùng",
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(113 + index)
			harness, _, _ := newPackagedPersonaBootstrapHarness(t, tt.working, revision)
			if tt.storedName != "" {
				if err := harness.env.a.st.SetAgentDisplayName(tt.storedName); err != nil {
					t.Fatal(err)
				}
			}
			state, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision)
			if err != nil {
				t.Fatal(err)
			}
			path := harness.env.a.zalo.cfg.PersonaPath
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, tt.working) {
				t.Fatalf("working Persona = %q, %v", got, err)
			}
			if _, err := os.Lstat(path + ".goc"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("completion eagerly published backup: %v", err)
			}
			wantFingerprint := agentPersonaFingerprint(tt.working, tt.wantName)
			if state.Phase != store.OnboardingPhaseTest || state.Revision != revision+1 ||
				state.PersonaFingerprint != wantFingerprint {
				t.Fatalf("state = %+v; want Test r+1 fingerprint %q", state, wantFingerprint)
			}
		})
	}
}

func TestPackagedPersonaNeverTrustsWritableIdentityOrProseForName(t *testing.T) {
	userPersona := []byte("Tôi tự nhận là Kẻ Giả Mạo nhưng đây là nội dung hoàn chỉnh.\n")
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, userPersona, 116)
	path := harness.env.a.zalo.cfg.PersonaPath
	if err := os.WriteFile(path+".goc", fixture.persona, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "identity.json"),
		[]byte(`{"display_name":"Kẻ Giả Mạo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint := agentPersonaFingerprint(userPersona, packagedPersonaTestName)
	if state.PersonaFingerprint != wantFingerprint {
		t.Fatalf("fingerprint = %q; want immutable identity binding %q", state.PersonaFingerprint, wantFingerprint)
	}
	if name, err := harness.env.a.st.AgentDisplayName(); err != nil || name != packagedPersonaTestName {
		t.Fatalf("display name = %q, %v", name, err)
	}
}

func TestPackagedPersonaRejectsUnknownHoleAndMismatchedBackupUntouched(t *testing.T) {
	for _, tt := range []struct {
		name    string
		persona []byte
		backup  []byte
	}{
		{"unknown hole", []byte("Không được {{SUY_DIEN}}.\n"), nil},
		{"mismatched backup before legacy migration", []byte("Tôi là {{TEN_BOT}}.\n"), []byte("không tin cậy")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			harness, _, revision := newPackagedPersonaBootstrapHarness(t, tt.persona, 121)
			path := harness.env.a.zalo.cfg.PersonaPath
			if tt.backup != nil {
				if err := os.WriteFile(path+".goc", tt.backup, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err == nil {
				t.Fatal("completePackagedOnboardingPersona() error = nil")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, before) {
				t.Fatalf("failure changed Persona: before=%q after=%q", before, after)
			}
			state := onboardingBootstrapState(t, harness)
			if state.Phase != store.OnboardingPhasePersona || state.Revision != revision {
				t.Fatalf("failure changed Store: %+v", state)
			}
		})
	}
}

func TestPackagedPersonaBackupAndRecoverySafety(t *testing.T) {
	t.Run("valid existing backup is idempotent", func(t *testing.T) {
		harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 124)
		path := harness.env.a.zalo.cfg.PersonaPath
		if err := os.WriteFile(path+".goc", fixture.persona, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path + ".goc")
		if err != nil || !bytes.Equal(got, fixture.persona) {
			t.Fatalf("backup = %q, %v", got, err)
		}
	})
	t.Run("backup directory fails before migration", func(t *testing.T) {
		harness, _, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 125)
		path := harness.env.a.zalo.cfg.PersonaPath
		if err := os.Mkdir(path+".goc", 0o700); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err == nil {
			t.Fatal("directory backup accepted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("backup failure changed Persona")
		}
		if err := os.Remove(path + ".goc"); err != nil {
			t.Fatal(err)
		}
		if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err != nil {
			t.Fatalf("retry after backup obstruction = %v", err)
		}
	})
	t.Run("backup symlink fails before migration", func(t *testing.T) {
		harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 1251)
		path := harness.env.a.zalo.cfg.PersonaPath
		target := filepath.Join(t.TempDir(), "outside-backup")
		if err := os.WriteFile(target, fixture.persona, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path+".goc"); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		before, _ := os.ReadFile(path)
		if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err == nil {
			t.Fatal("symlink backup accepted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("symlink backup failure changed Persona")
		}
	})
	t.Run("recovery pending fails untouched", func(t *testing.T) {
		harness, _, revision := newPackagedPersonaBootstrapHarness(t, fixtureLegacyBytes(), 126)
		path := harness.env.a.zalo.cfg.PersonaPath
		if err := os.WriteFile(agentPersonaRecoveryPath(path), []byte("malformed-private-recovery"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err == nil {
			t.Fatal("recovery pending accepted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("recovery failure changed Persona")
		}
	})
	t.Run("publication failure leaves immutable backup and retry is idempotent", func(t *testing.T) {
		harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, nil, 127)
		path := harness.env.a.zalo.cfg.PersonaPath
		originalWriter := writeAgentFileAtomic
		var attempts atomic.Int32
		writeAgentFileAtomic = func(string, []byte, os.FileMode) error {
			attempts.Add(1)
			return errors.New("PRIVATE_PUBLICATION_FAILURE")
		}
		if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err == nil {
			t.Fatal("publication failure returned nil")
		}
		writeAgentFileAtomic = originalWriter
		t.Cleanup(func() { writeAgentFileAtomic = originalWriter })
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed publication created working Persona: %v", err)
		}
		backup, err := os.ReadFile(path + ".goc")
		if err != nil || !bytes.Equal(backup, fixture.persona) {
			t.Fatalf("published backup = %q, %v", backup, err)
		}
		if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err != nil {
			t.Fatalf("retry = %v", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("injected writer attempts = %d", attempts.Load())
		}
	})
}

func fixtureLegacyBytes() []byte { return []byte("Tôi là {{TEN_BOT}}.\n") }

func TestPackagedPersonaRejectsMalformedWorkingFilesUntouched(t *testing.T) {
	tests := []struct {
		name    string
		persona []byte
	}{
		{"invalid UTF-8", []byte{0xff, 0xfe}},
		{"blank", []byte(" \t\n")},
		{"malformed braces", []byte("Bot Mặc Định {oops\n")},
		{"oversize", bytes.Repeat([]byte{'x'}, maxPersonaBytes+1)},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(128 + index)
			harness, _, _ := newPackagedPersonaBootstrapHarness(t, tt.persona, revision)
			path := harness.env.a.zalo.cfg.PersonaPath
			before, _ := os.ReadFile(path)
			if _, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision); err == nil {
				t.Fatal("malformed working Persona accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("malformed working Persona changed")
			}
		})
	}
}

func TestOnboardingBootstrapFromPersonaCompletesAtThirdSuccessor(t *testing.T) {
	harness, _, revision := newPackagedPersonaBootstrapHarness(t, nil, 131)
	installOnboardingBootstrapSeams(
		t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC), bytes.Repeat([]byte{0x31}, sha256.Size), time.Second,
	)
	rr := serveOnboardingBootstrap(onboardingBootstrapHandler(t, harness, nil), context.Background(), revision)
	if rr.Code != 200 {
		t.Fatalf("bootstrap status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if response.Phase != store.OnboardingPhaseCompleted || response.Revision != revision+3 {
		t.Fatalf("response = %+v; want Completed r+3", response)
	}
}

func TestPackagedPersonaCoreFailsClosedWithoutContextWorkingReader(t *testing.T) {
	tests := []struct {
		name   string
		reader appPersonaWorkingReader
		raw    string
	}{
		{name: "nil reader"},
		{
			name: "failing reader",
			raw:  "PRIVATE_PERSONA_READER_CAUSE",
			reader: func(string) ([]byte, bool, error) {
				return nil, false, errors.New("PRIVATE_PERSONA_READER_CAUSE")
			},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(133 + index)
			harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, nil, revision)
			harness.ctx.personaReader = tt.reader
			mux := http.NewServeMux()
			registerAppRoutesWithContext(mux, harness.ctx)
			harness.env.mux = mux
			beforeState := onboardingBootstrapState(t, harness)
			beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
			beforeAccounts := packagedPersonaAccountRows(t, harness.env)
			var probes atomic.Int32
			setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
				probes.Add(1)
				return "Xin chào, tôi là Bot Mặc Định.", nil
			})
			var logs syncLogBuffer
			harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

			rr := serveOnboardingBootstrap(
				onboardingBootstrapHandler(t, harness, nil), context.Background(), revision,
			)
			requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
			if probes.Load() != 0 {
				t.Fatalf("missing context reader executed %d provider probes", probes.Load())
			}
			if after := onboardingBootstrapState(t, harness); after != beforeState {
				t.Fatalf("missing context reader changed Store: before=%+v after=%+v", beforeState, after)
			}
			if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
				t.Fatalf("missing context reader changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
			}
			if afterAccounts := packagedPersonaAccountRows(t, harness.env); afterAccounts != beforeAccounts {
				t.Fatalf("missing context reader changed accounts:\nbefore=%s\nafter=%s", beforeAccounts, afterAccounts)
			}
			path := harness.env.a.zalo.cfg.PersonaPath
			for _, candidate := range []string{path, path + ".goc", agentPersonaRecoveryPath(path)} {
				if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing context reader published %q: %v", candidate, err)
				}
			}
			for _, private := range []string{
				tt.raw, fixture.root, path, string(fixture.persona), packagedPersonaTestName,
			} {
				if private != "" &&
					(strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private)) {
					t.Fatalf("missing context reader leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
				}
			}
		})
	}
}

func TestOnboardingBootstrapPersonaProbeFailureDurablyResumesFromTest(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, nil, 141)
	setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
		return "PRIVATE_ANSWER_SHOULD_NOT_LEAK", errors.New("PRIVATE_RAW_PROVIDER_CAUSE")
	})
	fixedNow := time.Date(2026, 8, 16, 12, 30, 0, 0, time.UTC)
	installOnboardingBootstrapSeams(t, fixedNow, bytes.Repeat([]byte{0x41}, sha256.Size), time.Second)
	beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
	beforeAccounts := packagedPersonaAccountRows(t, harness.env)
	beforeSnapshot, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(onboardingBootstrapHandler(t, harness, nil), context.Background(), revision)
	requireOnboardingCode(t, rr, http.StatusBadGateway, "ONBOARDING_TEST_FAILED")
	failed := onboardingBootstrapState(t, harness)
	if failed.Phase != store.OnboardingPhaseTest || failed.Revision != revision+1 ||
		failed.TestNonceHash != "" || failed.TestExpiresAt != "" {
		t.Fatalf("probe failure state = %+v; want durable Test r+1 without receipt", failed)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
		t.Fatalf("probe failure changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
	}
	if afterAccounts := packagedPersonaAccountRows(t, harness.env); afterAccounts != beforeAccounts {
		t.Fatalf("probe failure changed account ownership/enabled state:\nbefore=%s\nafter=%s", beforeAccounts, afterAccounts)
	}
	afterSnapshot, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(afterSnapshot.Stages) != len(beforeSnapshot.Stages) || len(afterSnapshot.Stages) == 0 ||
		afterSnapshot.Stages[0].AccountID != beforeSnapshot.Stages[0].AccountID ||
		afterSnapshot.Stages[0].Status != beforeSnapshot.Stages[0].Status {
		t.Fatalf("probe failure changed staging ownership: before=%+v after=%+v", beforeSnapshot.Stages, afterSnapshot.Stages)
	}
	for _, private := range []string{
		"PRIVATE_ANSWER_SHOULD_NOT_LEAK", "PRIVATE_RAW_PROVIDER_CAUSE", fixture.root,
		string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("response/log leaked private value %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}

	setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
		return "Xin chào, tôi là Bot Mặc Định.", nil
	})
	retry := serveOnboardingBootstrap(onboardingBootstrapHandler(t, harness, nil), context.Background(), revision+1)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingStatusResponse](t, retry)
	if response.Phase != store.OnboardingPhaseCompleted || response.Revision != revision+3 {
		t.Fatalf("retry response = %+v; want Completed original r+3", response)
	}
}

func TestOnboardingBootstrapTestResumeDoesNotLoadPersonaDefaults(t *testing.T) {
	harness := newOnboardingBootstrapHarness(
		t, []string{"future-cli"}, map[string]string{"future-cli": "future-model"}, 151,
		func(registrations []appProviderRuntimeRegistration) {
			future := runtimeOnboardingRegistration(t, registrations, "future-cli")
			future.NewOnboardingMember = func(*api, store.OnboardingTestRouteEntry) (appOnboardingMemberRun, error) {
				return func(context.Context, string, func(string)) (string, error) {
					return "Xin chào, tôi là Bé Mi.", nil
				}, nil
			}
		},
	)
	harness.ctx.personaDefaults = &appPersonaDefaultsSource{root: ""}
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, harness.ctx)
	harness.env.mux = mux
	installOnboardingBootstrapSeams(
		t, time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC), bytes.Repeat([]byte{0x51}, sha256.Size), time.Second,
	)
	rr := serveOnboardingBootstrap(onboardingBootstrapHandler(t, harness, nil), context.Background(), 151)
	if rr.Code != http.StatusOK {
		t.Fatalf("Test resume status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if response.Phase != store.OnboardingPhaseCompleted || response.Revision != 153 {
		t.Fatalf("Test resume = %+v; want r+2", response)
	}
}

func TestOnboardingBootstrapTestResumeResolvesCommittedPersonaRecoveryBeforeProbe(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, nil, 155)
	path := harness.env.a.zalo.cfg.PersonaPath
	if err := os.WriteFile(path, fixture.persona, 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := harness.env.a.prepareAgentPersonaRecovery(path, fixture.legacy, fixture.persona)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := harness.env.a.st.AdvanceOnboardingPersonaWithRecovery(
		revision,
		agentPersonaFingerprint(fixture.persona, packagedPersonaTestName),
		packagedPersonaTestName,
		token,
	)
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
		probes.Add(1)
		if _, err := os.Lstat(agentPersonaRecoveryPath(path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("provider probe began before committed recovery was finalized: %v", err)
		}
		return "Xin chào, tôi là Bot Mặc Định.", nil
	})
	installOnboardingBootstrapSeams(
		t, time.Date(2026, 8, 16, 13, 5, 0, 0, time.UTC),
		bytes.Repeat([]byte{0x55}, sha256.Size), time.Second,
	)

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), advanced.Revision,
	)
	if rr.Code != http.StatusOK {
		t.Fatalf("Test recovery resume status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if response.Phase != store.OnboardingPhaseCompleted || response.Revision != advanced.Revision+2 {
		t.Fatalf("Test recovery resume = %+v; want Completed Test r+2", response)
	}
	if probes.Load() != 1 {
		t.Fatalf("provider probes = %d; want 1", probes.Load())
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed Test recovery obligation remains: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.persona) {
		t.Fatalf("committed replacement changed during Test recovery: %q, %v", got, err)
	}
}

func TestOnboardingBootstrapTestResumeRejectsUnsafePersonaRecoveryBeforeProbe(t *testing.T) {
	const (
		token         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		previousToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		staleToken    = "cccccccccccccccccccccccccccccccc"
	)
	tests := []struct {
		name  string
		setup func(*testing.T, *runtimeOnboardingHarness, string, packagedPersonaFixture)
	}{
		{
			name: "corrupt",
			setup: func(t *testing.T, _ *runtimeOnboardingHarness, path string, _ packagedPersonaFixture) {
				if err := os.WriteFile(agentPersonaRecoveryPath(path), []byte("PRIVATE_CORRUPT_RECOVERY"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "uncommitted matching replacement",
			setup: func(t *testing.T, _ *runtimeOnboardingHarness, path string, fixture packagedPersonaFixture) {
				writeAgentRecoveryFixtureForTest(
					t, agentPersonaRecoveryPath(path), path, fixture.legacy, fixture.persona,
					token, "", agentRecoveryDomainForTest,
				)
			},
		},
		{
			name: "uncommitted bytes mismatch",
			setup: func(t *testing.T, _ *runtimeOnboardingHarness, path string, _ packagedPersonaFixture) {
				writeAgentRecoveryFixtureForTest(
					t, agentPersonaRecoveryPath(path), path,
					[]byte("Private original\n"), []byte("Private replacement\n"),
					token, "", agentRecoveryDomainForTest,
				)
			},
		},
		{
			name: "stale token lineage",
			setup: func(t *testing.T, harness *runtimeOnboardingHarness, path string, fixture packagedPersonaFixture) {
				writeAgentRecoveryFixtureForTest(
					t, agentPersonaRecoveryPath(path), path, fixture.legacy, fixture.persona,
					token, previousToken, agentRecoveryDomainForTest,
				)
				setAgentRecoveryTokenForTest(t, harness.env, staleToken)
			},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(157 + index)
			harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, nil, revision)
			path := harness.env.a.zalo.cfg.PersonaPath
			if err := os.WriteFile(path, fixture.persona, 0o600); err != nil {
				t.Fatal(err)
			}
			advanced, err := harness.env.a.st.AdvanceOnboardingPersona(
				revision,
				agentPersonaFingerprint(fixture.persona, packagedPersonaTestName),
				packagedPersonaTestName,
			)
			if err != nil {
				t.Fatal(err)
			}
			tt.setup(t, harness, path, fixture)
			sidecarBefore, err := os.ReadFile(agentPersonaRecoveryPath(path))
			if err != nil {
				t.Fatal(err)
			}
			beforeState := onboardingBootstrapState(t, harness)
			beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
			beforeAccounts := packagedPersonaAccountRows(t, harness.env)
			var probes atomic.Int32
			setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
				probes.Add(1)
				return "PRIVATE_UNSAFE_RECOVERY_PROBE", nil
			})
			var logs syncLogBuffer
			harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

			rr := serveOnboardingBootstrap(
				onboardingBootstrapHandler(t, harness, nil), context.Background(), advanced.Revision,
			)
			requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
			if probes.Load() != 0 {
				t.Fatalf("unsafe recovery executed %d provider probes", probes.Load())
			}
			if after := onboardingBootstrapState(t, harness); after != beforeState {
				t.Fatalf("unsafe recovery changed Store: before=%+v after=%+v", beforeState, after)
			}
			if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
				t.Fatalf("unsafe recovery changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
			}
			if afterAccounts := packagedPersonaAccountRows(t, harness.env); afterAccounts != beforeAccounts {
				t.Fatalf("unsafe recovery changed accounts:\nbefore=%s\nafter=%s", beforeAccounts, afterAccounts)
			}
			sidecarAfter, err := os.ReadFile(agentPersonaRecoveryPath(path))
			if err != nil || !bytes.Equal(sidecarAfter, sidecarBefore) {
				t.Fatalf("unsafe recovery changed sidecar: before=%q after=%q err=%v", sidecarBefore, sidecarAfter, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.persona) {
				t.Fatalf("unsafe recovery changed Persona: %q, %v", got, err)
			}
			for _, private := range []string{
				"PRIVATE_CORRUPT_RECOVERY", "PRIVATE_UNSAFE_RECOVERY_PROBE", path,
				string(sidecarBefore), string(fixture.persona), packagedPersonaTestName,
			} {
				if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
					t.Fatalf("unsafe recovery leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
				}
			}
		})
	}
}

func TestOnboardingBootstrapTestResumeRejectsTerminalPersonaReparseBeforeProbe(t *testing.T) {
	revision := int64(169)
	harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, nil, revision)
	externalDir := t.TempDir()
	externalPersona := filepath.Join(externalDir, "outside-persona.md")
	if err := os.WriteFile(externalPersona, fixture.persona, 0o600); err != nil {
		t.Fatal(err)
	}
	personaPath := harness.env.a.zalo.cfg.PersonaPath
	makePackagedPersonaTestFileLink(t, externalPersona, personaPath)
	harness.env.a.zalo.cfg.PersonaPath = personaPath
	advanced, err := harness.env.a.st.AdvanceOnboardingPersona(
		revision,
		agentPersonaFingerprint(fixture.persona, packagedPersonaTestName),
		packagedPersonaTestName,
	)
	if err != nil {
		t.Fatal(err)
	}
	beforeState := onboardingBootstrapState(t, harness)
	beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
	beforeAccounts := packagedPersonaAccountRows(t, harness.env)
	var probes atomic.Int32
	setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
		probes.Add(1)
		return "PRIVATE_TERMINAL_REPARSE_PROBE", nil
	})
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), advanced.Revision,
	)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
	if probes.Load() != 0 {
		t.Fatalf("terminal reparse executed %d provider probes", probes.Load())
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("terminal reparse changed Store: before=%+v after=%+v", beforeState, after)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
		t.Fatalf("terminal reparse changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
	}
	if afterAccounts := packagedPersonaAccountRows(t, harness.env); afterAccounts != beforeAccounts {
		t.Fatalf("terminal reparse changed accounts:\nbefore=%s\nafter=%s", beforeAccounts, afterAccounts)
	}
	if got, err := os.ReadFile(externalPersona); err != nil || !bytes.Equal(got, fixture.persona) {
		t.Fatalf("terminal reparse changed external Persona: %q, %v", got, err)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(personaPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal reparse created recovery sidecar: %v", err)
	}
	for _, private := range []string{
		"PRIVATE_TERMINAL_REPARSE_PROBE", externalDir, externalPersona, personaPath,
		string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("terminal reparse leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestOnboardingBootstrapTestResumeConsumesContextWorkingPersonaReaderBeforeProbe(t *testing.T) {
	revision := int64(170)
	harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, []byte(packagedPersonaTestText), revision)
	personaPath := harness.env.a.zalo.cfg.PersonaPath
	advanced, err := harness.env.a.st.AdvanceOnboardingPersona(
		revision,
		agentPersonaFingerprint(fixture.persona, packagedPersonaTestName),
		packagedPersonaTestName,
	)
	if err != nil {
		t.Fatal(err)
	}
	harness.ctx.personaReader = func(path string) ([]byte, bool, error) {
		if path == personaPath {
			return nil, false, errAppPersonaDefaultsInvalid
		}
		return readAppWorkingPersona(path)
	}
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, harness.ctx)
	harness.env.mux = mux
	beforeState := onboardingBootstrapState(t, harness)
	beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
	beforeAccounts := packagedPersonaAccountRows(t, harness.env)
	var probes atomic.Int32
	setPackagedPersonaTestRunner(harness, func(context.Context, string, func(string)) (string, error) {
		probes.Add(1)
		return "Xin chào, tôi là Bot Mặc Định.", nil
	})
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), advanced.Revision,
	)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
	if probes.Load() != 0 {
		t.Fatalf("strict-reader rejection executed %d provider probes", probes.Load())
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("strict-reader rejection changed Store: before=%+v after=%+v", beforeState, after)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
		t.Fatalf("strict-reader rejection changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
	}
	if afterAccounts := packagedPersonaAccountRows(t, harness.env); afterAccounts != beforeAccounts {
		t.Fatalf("strict-reader rejection changed accounts:\nbefore=%s\nafter=%s", beforeAccounts, afterAccounts)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(personaPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict-reader rejection created recovery sidecar: %v", err)
	}
	for _, private := range []string{
		personaPath, string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("strict-reader rejection leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestOnboardingBootstrapPersonaCancellationDoesNotAdvanceStore(t *testing.T) {
	harness, _, revision := newPackagedPersonaBootstrapHarness(t, nil, 161)
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	rr := serveOnboardingBootstrap(onboardingBootstrapHandler(t, harness, nil), requestContext, revision)
	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled status=%d body=%s", rr.Code, rr.Body.String())
	}
	state := onboardingBootstrapState(t, harness)
	if state.Phase != store.OnboardingPhasePersona || state.Revision != revision {
		t.Fatalf("cancellation advanced Store: %+v", state)
	}
}

func TestAppAgentPersonaMutationPathsPublishOnlyImmutableDefaultBackup(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		body      func(int64) string
		working   []byte
		want      []byte
		wantPhase string
	}{
		{
			name: "full Persona PUT", path: "/agent/persona/persona",
			body:    func(int64) string { return `{"text":"Nội dung mới hoàn chỉnh.\n"}` },
			working: []byte("Nội dung người dùng đã sửa hoàn chỉnh.\n"),
			want:    []byte("Nội dung mới hoàn chỉnh.\n"), wantPhase: store.OnboardingPhasePersona,
		},
		{
			name: "normal legacy agent PUT", path: "/agent",
			body:    func(int64) string { return `{"values":{"TEN_CHUYEN_GIA":"An Nhiên"}}` },
			working: []byte("Bot Mặc Định hỏi {{TEN_CHUYEN_GIA}}.\n"),
			want:    []byte("Bot Mặc Định hỏi An Nhiên.\n"), wantPhase: store.OnboardingPhasePersona,
		},
		{
			name: "complete legacy agent PUT", path: "/agent",
			body: func(revision int64) string {
				return fmt.Sprintf(`{"values":{"TEN_CHUYEN_GIA":"An Nhiên"},"display_name":"Bot Mặc Định","require_complete":true,"onboarding_revision":%d}`, revision)
			},
			working: []byte("Bot Mặc Định hỏi {{TEN_CHUYEN_GIA}}.\n"),
			want:    []byte("Bot Mặc Định hỏi An Nhiên.\n"), wantPhase: store.OnboardingPhaseTest,
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(171 + index*10)
			harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, tt.working, revision)
			if err := harness.env.a.st.SetAgentDisplayName(packagedPersonaTestName); err != nil {
				t.Fatal(err)
			}
			rr := harness.env.serve(http.MethodPut, tt.path, tt.body(revision))
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			path := harness.env.a.zalo.cfg.PersonaPath
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, tt.want) {
				t.Fatalf("working = %q, %v; want %q", got, err, tt.want)
			}
			backup, err := os.ReadFile(path + ".goc")
			if err != nil || !bytes.Equal(backup, fixture.persona) {
				t.Fatalf("backup = %q, %v; want immutable default", backup, err)
			}
			if state := onboardingBootstrapState(t, harness); state.Phase != tt.wantPhase {
				t.Fatalf("state = %+v; want phase %s", state, tt.wantPhase)
			}
		})
	}
}

func TestAppAgentPersonaFullPutFailsClosedWithoutDefaultsDependency(t *testing.T) {
	working := []byte("Nội dung người dùng đã sửa hoàn chỉnh.\n")
	harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, working, 191)
	harness.ctx.personaDefaults = nil
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, harness.ctx)
	harness.env.mux = mux
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	beforeState := onboardingBootstrapState(t, harness)
	rr := harness.env.serve(
		http.MethodPut, "/agent/persona/persona", `{"text":"Nội dung mới hoàn chỉnh.\n"}`,
	)
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "PERSONA_DEFAULTS_INVALID")
	path := harness.env.a.zalo.cfg.PersonaPath
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, working) {
		t.Fatalf("missing dependency changed working Persona: %q, %v", got, err)
	}
	if _, err := os.Lstat(path + ".goc"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dependency published backup: %v", err)
	}
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("missing dependency changed Store: before=%+v after=%+v", beforeState, after)
	}
	for _, private := range []string{path, string(working), string(fixture.persona), packagedPersonaTestName} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("missing dependency leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestAppAgentPersonaMutationPathsRejectMismatchedBackupBeforeWrite(t *testing.T) {
	tests := []struct {
		name string
		path string
		body func(int64) string
	}{
		{"full", "/agent/persona/persona", func(int64) string { return `{"text":"new\n"}` }},
		{"normal", "/agent", func(int64) string { return `{"values":{"TEN_CHUYEN_GIA":"An Nhiên"}}` }},
		{"complete", "/agent", func(revision int64) string {
			return fmt.Sprintf(`{"values":{"TEN_CHUYEN_GIA":"An Nhiên"},"display_name":"Bot Mặc Định","require_complete":true,"onboarding_revision":%d}`, revision)
		}},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(201 + index*10)
			working := []byte("Bot Mặc Định hỏi {{TEN_CHUYEN_GIA}}.\n")
			harness, _, _ := newPackagedPersonaBootstrapHarness(t, working, revision)
			path := harness.env.a.zalo.cfg.PersonaPath
			if err := os.WriteFile(path+".goc", []byte("mutable untrusted backup"), 0o600); err != nil {
				t.Fatal(err)
			}
			backupBefore, _ := os.ReadFile(path + ".goc")
			var logs syncLogBuffer
			harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
			beforeState := onboardingBootstrapState(t, harness)
			rr := harness.env.serve(http.MethodPut, tt.path, tt.body(revision))
			if rr.Code < 400 {
				t.Fatalf("status=%d body=%s; want safe failure", rr.Code, rr.Body.String())
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, working) {
				t.Fatalf("failure changed working Persona: %q", got)
			}
			backupAfter, _ := os.ReadFile(path + ".goc")
			if !bytes.Equal(backupAfter, backupBefore) {
				t.Fatalf("failure overwrote backup: before=%q after=%q", backupBefore, backupAfter)
			}
			if after := onboardingBootstrapState(t, harness); after != beforeState {
				t.Fatalf("failure changed Store: before=%+v after=%+v", beforeState, after)
			}
			for _, private := range []string{path, string(working), string(backupBefore), packagedPersonaTestName} {
				if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
					t.Fatalf("failure leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
				}
			}
		})
	}
}

func TestAppAgentPersonaMutationPathsRejectReparseAncestorBeforeWrite(t *testing.T) {
	tests := []struct {
		name string
		path string
		body func(int64) string
	}{
		{"full", "/agent/persona/persona", func(int64) string {
			return `{"text":"Nội dung mới hoàn chỉnh.\n"}`
		}},
		{"normal", "/agent", func(int64) string {
			return `{"values":{"TEN_CHUYEN_GIA":"An Nhiên"}}`
		}},
		{"complete", "/agent", func(revision int64) string {
			return fmt.Sprintf(
				`{"values":{"TEN_CHUYEN_GIA":"An Nhiên"},"display_name":"Bot Mặc Định","require_complete":true,"onboarding_revision":%d}`,
				revision,
			)
		}},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(211 + index)
			working := []byte("Bot Mặc Định hỏi {{TEN_CHUYEN_GIA}}.\n")
			harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, working, revision)

			external := t.TempDir()
			externalPersona := filepath.Join(external, "persona.md")
			if err := os.WriteFile(externalPersona, working, 0o600); err != nil {
				t.Fatal(err)
			}
			linkParent := t.TempDir()
			linkedPersonaDir := filepath.Join(linkParent, "persona-link")
			makeOnboardingTestDirLink(t, external, linkedPersonaDir)
			harness.env.a.zalo.cfg.PersonaPath = filepath.Join(linkedPersonaDir, "persona.md")

			beforeState := onboardingBootstrapState(t, harness)
			var logs syncLogBuffer
			harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
			rr := harness.env.serve(http.MethodPut, tt.path, tt.body(revision))
			requireOnboardingCode(t, rr, http.StatusInternalServerError, "PERSONA_DEFAULTS_INVALID")

			got, err := os.ReadFile(externalPersona)
			if err != nil || !bytes.Equal(got, working) {
				t.Fatalf("reparse rejection changed external Persona: %q, %v", got, err)
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "persona.md" {
				t.Fatalf("reparse rejection created external files: %v", entries)
			}
			if after := onboardingBootstrapState(t, harness); after != beforeState {
				t.Fatalf("reparse rejection changed Store: before=%+v after=%+v", beforeState, after)
			}
			for _, private := range []string{
				external, linkedPersonaDir, externalPersona, string(working),
				string(fixture.persona), packagedPersonaTestName,
			} {
				if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
					t.Fatalf("reparse rejection leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
				}
			}
		})
	}
}

func TestAppAgentPersonaCompleteRejectsTerminalReparseBeforeNoopAdvance(t *testing.T) {
	revision := int64(219)
	harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, nil, revision)
	externalDir := t.TempDir()
	externalPersona := filepath.Join(externalDir, "outside-persona.md")
	if err := os.WriteFile(externalPersona, fixture.persona, 0o600); err != nil {
		t.Fatal(err)
	}
	personaPath := harness.env.a.zalo.cfg.PersonaPath
	makePackagedPersonaTestFileLink(t, externalPersona, personaPath)
	harness.env.a.zalo.cfg.PersonaPath = personaPath
	beforeState := onboardingBootstrapState(t, harness)
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	body := fmt.Sprintf(
		`{"values":{},"display_name":"Bot Mặc Định","require_complete":true,"onboarding_revision":%d}`,
		revision,
	)

	rr := harness.env.serve(http.MethodPut, "/agent", body)
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "PERSONA_DEFAULTS_INVALID")
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("terminal reparse advanced Store: before=%+v after=%+v", beforeState, after)
	}
	if got, err := os.ReadFile(externalPersona); err != nil || !bytes.Equal(got, fixture.persona) {
		t.Fatalf("terminal reparse changed external Persona: %q, %v", got, err)
	}
	if _, err := os.Lstat(personaPath + ".goc"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal reparse published backup: %v", err)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(personaPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal reparse created recovery sidecar: %v", err)
	}
	for _, private := range []string{
		externalDir, externalPersona, personaPath, string(fixture.persona), packagedPersonaTestName,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("terminal reparse leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestAppAgentPersonaCompleteConsumesContextWorkingPersonaReaderBeforeNoopAdvance(t *testing.T) {
	revision := int64(220)
	harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, []byte(packagedPersonaTestText), revision)
	personaPath := harness.env.a.zalo.cfg.PersonaPath
	harness.ctx.personaReader = func(path string) ([]byte, bool, error) {
		if path == personaPath {
			return nil, false, errAppPersonaDefaultsInvalid
		}
		return readAppWorkingPersona(path)
	}
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, harness.ctx)
	harness.env.mux = mux
	beforeState := onboardingBootstrapState(t, harness)
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	body := fmt.Sprintf(
		`{"values":{},"display_name":"Bot Mặc Định","require_complete":true,"onboarding_revision":%d}`,
		revision,
	)

	rr := harness.env.serve(http.MethodPut, "/agent", body)
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "PERSONA_DEFAULTS_INVALID")
	if after := onboardingBootstrapState(t, harness); after != beforeState {
		t.Fatalf("strict-reader rejection advanced Store: before=%+v after=%+v", beforeState, after)
	}
	if got, err := os.ReadFile(personaPath); err != nil || !bytes.Equal(got, fixture.persona) {
		t.Fatalf("strict-reader rejection changed Persona: %q, %v", got, err)
	}
	if _, err := os.Lstat(personaPath + ".goc"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict-reader rejection published backup: %v", err)
	}
	if _, err := os.Lstat(agentPersonaRecoveryPath(personaPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict-reader rejection created recovery sidecar: %v", err)
	}
	for _, private := range []string{personaPath, string(fixture.persona), packagedPersonaTestName} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("strict-reader rejection leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func TestAppAgentPersonaMutationWriterFailuresArePrivate(t *testing.T) {
	tests := []struct {
		name string
		path string
		body func(int64) string
	}{
		{"full", "/agent/persona/persona", func(int64) string { return `{"text":"Nội dung mới hoàn chỉnh.\n"}` }},
		{"normal", "/agent", func(int64) string { return `{"values":{"TEN_CHUYEN_GIA":"An Nhiên"}}` }},
		{"complete", "/agent", func(revision int64) string {
			return fmt.Sprintf(`{"values":{"TEN_CHUYEN_GIA":"An Nhiên"},"display_name":"Bot Mặc Định","require_complete":true,"onboarding_revision":%d}`, revision)
		}},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(221 + index)
			working := []byte("Bot Mặc Định hỏi {{TEN_CHUYEN_GIA}}.\n")
			harness, fixture, _ := newPackagedPersonaBootstrapHarness(t, working, revision)
			var logs syncLogBuffer
			harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
			originalWriter := writeAgentFileAtomic
			writeAgentFileAtomic = func(string, []byte, os.FileMode) error {
				return errors.New("PRIVATE_WRITER_CAUSE")
			}
			t.Cleanup(func() { writeAgentFileAtomic = originalWriter })
			rr := harness.env.serve(http.MethodPut, tt.path, tt.body(revision))
			writeAgentFileAtomic = originalWriter
			if rr.Code < 400 {
				t.Fatalf("writer failure status=%d body=%s", rr.Code, rr.Body.String())
			}
			for _, private := range []string{
				"PRIVATE_WRITER_CAUSE", harness.env.a.zalo.cfg.PersonaPath,
				string(working), string(fixture.persona), packagedPersonaTestName,
			} {
				if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
					t.Fatalf("writer failure leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
				}
			}
		})
	}
}

func TestAppAgentPersonaCompleteStaleRevisionHasCASPrecedenceAndNoBackupSideEffect(t *testing.T) {
	working := []byte("Bot Mặc Định hỏi {{TEN_CHUYEN_GIA}}.\n")
	harness, _, revision := newPackagedPersonaBootstrapHarness(t, working, 231)
	path := harness.env.a.zalo.cfg.PersonaPath
	body := fmt.Sprintf(
		`{"values":{"TEN_CHUYEN_GIA":"An Nhiên"},"display_name":"Bot Mặc Định","require_complete":true,"onboarding_revision":%d}`,
		revision-1,
	)
	rr := harness.env.serve(http.MethodPut, "/agent", body)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, working) {
		t.Fatalf("stale request changed working Persona: %q, %v", got, err)
	}
	if _, err := os.Lstat(path + ".goc"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale request published backup before CAS: %v", err)
	}
}

func TestOnboardingBootstrapPersonaSeparatesRepairConflictFromPackageFailure(t *testing.T) {
	t.Run("working repair required is sanitized conflict", func(t *testing.T) {
		harness, fixture, revision := newPackagedPersonaBootstrapHarness(
			t, []byte("Không được {{SUY_DIEN}}.\n"), 241,
		)
		var logs syncLogBuffer
		harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
		rr := serveOnboardingBootstrap(onboardingBootstrapHandler(t, harness, nil), context.Background(), revision)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
		for _, private := range []string{fixture.root, string(fixture.persona), "SUY_DIEN", packagedPersonaTestName} {
			if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
				t.Fatalf("repair conflict leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
			}
		}
	})
	t.Run("immutable package failure is sanitized internal error", func(t *testing.T) {
		harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, nil, 251)
		if err := os.WriteFile(filepath.Join(fixture.root, "build-manifest.json"), []byte("PRIVATE_BAD_MANIFEST"), 0o600); err != nil {
			t.Fatal(err)
		}
		var logs syncLogBuffer
		harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
		rr := serveOnboardingBootstrap(onboardingBootstrapHandler(t, harness, nil), context.Background(), revision)
		requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_PERSONA_DEFAULTS_INVALID")
		for _, private := range []string{fixture.root, "PRIVATE_BAD_MANIFEST", string(fixture.persona), packagedPersonaTestName} {
			if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
				t.Fatalf("package failure leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
			}
		}
	})
}

func TestPackagedPersonaPreservesSnapshotAndStoreErrorCategories(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *runtimeOnboardingHarness)
		status int
		code   string
	}{
		{
			name: "Store read failure", status: http.StatusInternalServerError, code: "ONBOARDING_STATE_UNAVAILABLE",
			mutate: func(t *testing.T, harness *runtimeOnboardingHarness) {
				if err := harness.env.a.st.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "snapshot configuration drift", status: http.StatusConflict, code: "ONBOARDING_CONFIGURATION_CHANGED",
			mutate: func(t *testing.T, harness *runtimeOnboardingHarness) {
				if _, err := harness.env.db.Exec(`UPDATE app_onboarding_provider_stages SET position=7`); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			revision := int64(261 + index)
			harness, _, _ := newPackagedPersonaBootstrapHarness(t, nil, revision)
			var logs syncLogBuffer
			harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
			tt.mutate(t, harness)
			_, err := harness.ctx.completePackagedOnboardingPersona(context.Background(), revision)
			if err == nil {
				t.Fatal("completePackagedOnboardingPersona() error = nil")
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/onboarding/bootstrap", nil)
			harness.ctx.writePackagedPersonaBootstrapError(recorder, request, err)
			requireOnboardingCode(t, recorder, tt.status, tt.code)
			if strings.Contains(recorder.Body.String(), err.Error()) || strings.Contains(logs.String(), err.Error()) {
				t.Fatalf("public error leaked raw cause %q: body=%s logs=%s", err, recorder.Body.String(), logs.String())
			}
		})
	}
}

func TestOnboardingBootstrapPersonaPublicSnapshotDriftIsConfigurationConflict(t *testing.T) {
	harness, fixture, revision := newPackagedPersonaBootstrapHarness(t, nil, 271)
	if _, err := harness.env.db.Exec(`UPDATE app_onboarding_provider_stages SET position=7`); err != nil {
		t.Fatal(err)
	}
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), revision,
	)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_CONFIGURATION_CHANGED")
	for _, private := range []string{
		fixture.root, string(fixture.persona), packagedPersonaTestName, harness.env.a.zalo.cfg.PersonaPath,
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("configuration conflict leaked %q: body=%s logs=%s", private, rr.Body.String(), logs.String())
		}
	}
}

func packagedPersonaAccountRows(t *testing.T, env *onboardingRouteTestEnv) string {
	t.Helper()
	rows, err := env.db.Query(`SELECT id, provider_id, config_dir, enabled FROM llm_accounts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result strings.Builder
	for rows.Next() {
		var id, providerID, configDir string
		var enabled int
		if err := rows.Scan(&id, &providerID, &configDir, &enabled); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&result, "%q|%q|%q|%d;", id, providerID, configDir, enabled)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result.String()
}

func setPackagedPersonaTestRunner(
	harness *runtimeOnboardingHarness,
	run appOnboardingMemberRun,
) {
	registration := harness.ctx.registry.byKind["future-cli"]
	registration.NewOnboardingMember = func(*api, store.OnboardingTestRouteEntry) (appOnboardingMemberRun, error) {
		return run, nil
	}
	harness.ctx.registry.byKind["future-cli"] = registration
}

func newPackagedPersonaBootstrapHarness(
	t *testing.T,
	existing []byte,
	revision int64,
) (*runtimeOnboardingHarness, packagedPersonaFixture, int64) {
	t.Helper()
	fixture := newPackagedPersonaFixture(t)
	var calls atomic.Int32
	harness := newRuntimeOnboardingHarness(t, func(registrations []appProviderRuntimeRegistration) {
		future := runtimeOnboardingRegistration(t, registrations, "future-cli")
		future.NewOnboardingMember = func(*api, store.OnboardingTestRouteEntry) (appOnboardingMemberRun, error) {
			return func(context.Context, string, func(string)) (string, error) {
				calls.Add(1)
				return "Xin chào, tôi là Bot Mặc Định.", nil
			}, nil
		}
	})
	snapshot := harness.prepareReadyRoute(t, []string{"future-cli"}, map[string]string{"future-cli": "future-model"})
	personaPath := filepath.Join(t.TempDir(), "persona.md")
	if existing != nil {
		if err := os.WriteFile(personaPath, existing, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configureAgentPersonaForOnboardingTest(t, harness.env, string(fixture.persona))
	harness.env.a.zalo.cfg.PersonaPath = personaPath
	state := snapshot.State
	state.Phase = store.OnboardingPhasePersona
	state.ProviderKind = ""
	state.ProviderID = ""
	state.AccountID = ""
	state.ModelID = ""
	state.PersonaFingerprint = ""
	state.TestNonceHash = ""
	state.TestExpiresAt = ""
	state.Revision = revision
	harness.env.setState(t, state)
	harness.ctx.personaDefaults = &appPersonaDefaultsSource{root: fixture.root}
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, harness.ctx)
	harness.env.mux = mux
	return harness, fixture, revision
}

func makePackagedPersonaTestFileLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("test file link is not a symlink: mode=%v", info.Mode())
	}
}
