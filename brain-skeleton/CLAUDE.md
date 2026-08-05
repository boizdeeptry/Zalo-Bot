# CLAUDE.md — Wiki Schema

> **Hướng dẫn cho người mới:** Đổi `<Vault Name>` ở phần dưới thành tên vault của bạn (vd: `My Brain`, `Sales Vault`). Phần còn lại của file này là schema chuẩn — chỉ sửa khi bạn đã ingest 5-10 source và rút ra convention riêng.

Vault name: **<Vault Name>**

This vault is a **persistent, LLM-maintained wiki**. Knowledge is compiled once when sources arrive and kept current — not re-derived on every query.

Read this file at the start of every session before reading or writing other files.

## Three layers

1. **Raw sources** (`raw/`) — immutable input documents (clipped articles, PDFs, notes, transcripts, images). Read from here, never modify.
2. **Wiki** (`wiki/`) — LLM-generated markdown pages: source summaries, entity pages, concept pages, syntheses, comparisons, analyses. The LLM owns this layer entirely.
3. **Schema** (this file) — conventions and workflows. Co-evolved with the user over time.

Plus two navigation files at the root:
- `wiki/index.md` — content catalog (what pages exist, organized by category). Lives INSIDE
  `wiki/` on purpose: the answering bot is granted `wiki/` and `raw/` only, so a catalog at the
  vault root would be unreachable by the very agent it exists to help.
- `log.md` — append-only chronological record (ingests, queries, lint passes)

## Directory structure

```
/
├── CLAUDE.md              # this file
├── wiki/index.md         # content catalog
├── log.md                 # chronological log
├── raw/
│   ├── assets/            # downloaded images, PDFs
│   └── ...                # source documents
├── wiki/
│   ├── sources/           # one summary page per ingested source
│   ├── entities/          # people, companies, organizations, products, projects
│   ├── concepts/          # frameworks, ideas, methodologies, principles
│   ├── topics/            # cross-cutting topic syntheses
│   └── analyses/          # query results filed back as pages
└── reference/             # docs about the system itself, not knowledge content
```

Subfolders inside `wiki/` are suggestions, not rigid. Add new categories when the domain calls for it (e.g., `events/`, `decisions/`, `books/`). Update this section when you do.

## File conventions

- **Filenames**: human-readable titles with spaces are fine (Obsidian handles them). Use the canonical name of the entity, concept, or source.
- **Wikilinks**: link aggressively with `[[Page Name]]`. Every entity, concept, and source mentioned in prose should be a link.
- **Frontmatter**: every wiki page starts with YAML frontmatter:
  ```yaml
  ---
  type: source | entity | concept | topic | analysis
  tags: [tag1, tag2]
  created: 2026-04-26
  updated: 2026-04-26
  sources: ["[[Source A]]", "[[Source B]]"]   # wiki source pages it draws from
  ---
  ```
- **Source pages** also include: `raw_path: raw/...`, `author`, `date`, `url` (when applicable).
- **Citations**: in wiki text, cite sources with `[[Source Title]]` inline. Avoid claims without a source link.
- **Language**: write each page in the language of its sources. If sources are mixed, the dominant language wins; preserve original quotes verbatim.

## Workflow: Ingest

When the user says "ingest X" or drops a file in `raw/`:

1. **Read** the source in full. For images, view them; for long files, read in chunks.
2. **Discuss key takeaways** with the user (3-5 bullet points). Ask what to emphasize before writing.
3. **Write the source summary** at `wiki/sources/<Source Title>.md` — frontmatter, 1-paragraph TL;DR, key claims with section/page refs, list of entities and concepts mentioned.
4. **Update or create entity pages** for each named entity. New entity → create `wiki/entities/<Name>.md`. Existing entity → append to the relevant section, link back to the new source. Add subfolders when your domain needs them (e.g. `wiki/clients/`, `wiki/projects/`, `wiki/authors/`).
5. **Update or create concept pages** under `wiki/concepts/` (frameworks, methodologies, recurring ideas). Add other wiki categories when needed (e.g. `wiki/courses/`, `wiki/stories/`, `wiki/papers/`) and document them in this file.
6. **Note contradictions** explicitly. If a new source disagrees with an existing claim, add a "Contradictions" section on the relevant page with both views and source links — don't silently overwrite.
7. **Update `wiki/index.md`** — add the new source page and any new entity/concept pages.
8. **Append to `log.md`** — see Log format below.

A single source typically touches 5-15 wiki pages. After ingestion, summarize what changed for the user.

## Workflow: Query

When the user asks a question against the wiki:

1. **Read `wiki/index.md`** first to find candidate pages.
2. **Drill into relevant pages**. Follow wikilinks to gather context.
3. **Synthesize an answer with citations** — every claim links to a wiki page (or, for direct quotes, a raw source).
4. **Pick the right output format** for the question: prose, comparison table, bullet list, Marp slide deck (`.md` with `marp: true` frontmatter), matplotlib chart, canvas, etc.
5. **Offer to file the answer back** as a new page under `wiki/analyses/` if it's a non-trivial synthesis worth keeping. The user decides.

If the wiki doesn't have enough to answer, say so — don't fabricate. Suggest sources to ingest or web searches to run.

## Workflow: Lint

When the user says "lint the wiki" or periodically:

Health-check pass. Look for and report:
- **Contradictions** between pages on the same entity or concept.
- **Stale claims** — pages that newer sources have superseded but weren't updated.
- **Orphan pages** — no inbound wikilinks (use Obsidian graph or `grep`).
- **Missing pages** — concepts mentioned in 3+ sources but lacking their own page.
- **Missing cross-references** — entity A and entity B appear together repeatedly but neither page links to the other.
- **Index drift** — pages that exist on disk but aren't in `wiki/index.md`, or vice versa.
- **Open questions** — gaps the wiki has identified but not answered. Suggest sources or searches.

Report findings as a checklist. Don't fix silently; let the user prioritize.

## wiki/index.md format

Organized by category. Each entry is one line:

```markdown
- [[Source Title]] — one-line summary (date, author)
- [[Entity Name]] — one-line description
```

Sections: Sources, Entities, Concepts, Topics, Analyses. Add categories as the wiki grows.

## log.md format

Append-only. Each entry starts with a consistent prefix so it's greppable:

```markdown
## [2026-04-26] ingest | Article Title
- Created: [[Source Title]]
- Updated: [[Entity A]], [[Concept B]]
- Notes: contradicts earlier claim in [[Entity A]]; user emphasized X.

## [2026-04-26] query | "How does X relate to Y?"
- Read: [[Page1]], [[Page2]], [[Page3]]
- Output: comparison table, filed as [[wiki/analyses/X vs Y]]

## [2026-04-26] lint
- 3 orphan pages: ...
- 1 contradiction: ...
```

Newest entries at the bottom. Quick check: `grep "^## \[" log.md | tail -10`.

## Tips

- **Obsidian Web Clipper** drops articles into `raw/` as markdown.
- **Image downloads**: in Obsidian set attachment folder to `raw/assets/`. After clipping, run "Download attachments for current file" to pull images locally — view them when context matters.
- **Graph view** shows wiki shape — hubs and orphans.
- **Marp** for slide decks (`marp: true` in frontmatter).
- **Dataview** queries page frontmatter — keep YAML clean and consistent.

## Co-evolving this file

When Claude discovers a workflow improvement, a naming convention that breaks down, or a recurring user preference, propose an edit to this file and ask the user to confirm. The schema is supposed to drift toward what works for this vault — not stay static.
