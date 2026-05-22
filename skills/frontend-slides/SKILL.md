---
name: frontend-slides
description: Template-based HTML presentation generator. 34 curated templates. One skill_run call = finished HTML file.
---

# Frontend Slides — One-Call Generation

Generate a complete HTML presentation with **a single `skill_run` call**. Templates handle all design; you only provide content.

## ⚡ The One-Call Pattern

```
skill_run(
  skill="frontend-slides",
  action="generate",
  slug="blue-professional",
  title="Your Title",
  subtitle="Your Subtitle",
  author="Author Name",
  date="May 2026",
  slides=[
    {"layout": "cover", "heading": "Title Here", "body": "Subtitle text"},
    {"layout": "metrics", "heading": "Key Points", "items": ["Point 1", "Point 2", "Point 3"]},
    {"layout": "closing", "heading": "Thank You", "body": "Contact info or CTA"}
  ],
  output_path="C:\\path\\to\\output.html"
)
```

That's it. One call → finished HTML file. No reading templates, no file_write, no HTML editing.

## How to Choose a Template

**ALWAYS let the user choose. NEVER pick a template yourself.**

1. Use `match_templates` to find top 5 matching templates based on topic/mood
2. Present them to the user via an interactive choice card:
```interactive
{"type":"choice","id":"template_slug","question":"请选择演示文稿模板风格","options":["blue-professional — Business/Tech","signal — Modern Tech","..."]}
```
3. Wait for user selection, then use the chosen slug in `generate` action

**Template categories** (for match_templates hints, NOT for auto-selection):
- **Business/Tech**: `blue-professional`, `signal`, `cartesian`, `studio`
- **Creative/Bold**: `bold-poster`, `creative-mode`, `neo-grid-bold`, `coral`
- **Editorial**: `editorial-forest`, `vellum`, `broadside`
- **Warm/Friendly**: `capsule`, `daisy-days`, `playful`, `grove`
- **Dark/Cyber**: `8-bit-orbit`, `pink-script`, `mat`, `retro-windows`
- **Minimal**: `monochrome`, `cobalt-grid`, `raw-grid`
Returns top 5 matches with slug and match score.

## Slide Layouts

Each slide has a `layout` field. Available layouts vary by template but commonly include:

| Layout | Use For | Content Fields |
|--------|---------|----------------|
| `cover` | Title slide (auto for slide 0) | heading, body |
| `agenda` | Table of contents | heading, items |
| `metrics` | Key numbers / 3-column cards | heading, items |
| `dashboard` | 6-stat grid | heading, items |
| `split` | Two-column analysis | heading, body, items |
| `bars` | Horizontal bar chart | heading, items |
| `quote` | Big quote | heading, body |
| `timeline` | Process flow | heading, items |
| `detail` | Detailed bullets | heading, items |
| `closing` | End slide (auto for last slide) | heading, body |

**Auto-layout**: If you omit `layout`, slide 0 = `cover`, last slide = `closing`, middle slides = `metrics`.

## Content Fields

- `heading` (string): Slide title — replaces `<h1>` or `<h2>`
- `body` (string): Body text — replaces `<p>` with subtitle/body class
- `items` (array of strings): Bullet items — replaces `<li>` elements

## Advanced: Custom Replacements

For fine-grained control, use the `replacements` parameter (old_text → new_text pairs):

```
skill_run(
  skill="frontend-slides",
  action="generate",
  slug="blue-professional",
  title="My Presentation",
  replacements={
    "Q1 Revenue": "$2.4M Revenue",
    "47%": "62%",
    "Author Name": "Zhang San"
  },
  output_path="C:\\output.html"
)
```

## Other Actions

| Action | When to Use |
|--------|-------------|
| `match_templates` | User wants to browse templates by mood/topic |
| `read_template` | Need to inspect template HTML for advanced customization |
| `list_templates` | Show all 34 templates |
| `info` | Quick skill info |

## ⚠️ Critical Rules

1. **ALWAYS use `generate` action** — never use file_write to create HTML from scratch
2. **NEVER write CSS/JS** — templates already have complete styling
3. **One call = one presentation** — don't split into multiple steps
4. **After generation**, use `code_run` to open: `os.startfile(r"C:\path\to\output.html")`
