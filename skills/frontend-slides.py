import sys
import json
import os
import re
import shutil
import subprocess

SKILL_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "frontend-slides")
TEMPLATES_DIR = os.path.join(SKILL_DIR, "templates")

def main():
    if len(sys.argv) < 2:
        print(json.dumps({"status": "error", "msg": "No arguments provided"}, ensure_ascii=False))
        sys.exit(1)

    args = json.loads(sys.argv[1])
    action = args.get("action", "info")

    handlers = {
        "info": do_info,
        "list_templates": do_list_templates,
        "match_templates": do_match_templates,
        "read_template": do_read_template,
        "read_file": do_read_file,
        "generate": do_generate,
        "extract_pptx": do_extract_pptx,
        "list_files": do_list_files,
    }

    handler = handlers.get(action)
    if handler:
        handler(args)
    else:
        print(json.dumps({"status": "error", "msg": f"Unknown action: {action}. Available: {', '.join(handlers.keys())}"}, ensure_ascii=False))
        sys.exit(1)

def do_info(args):
    index = load_index()
    print(json.dumps({
        "status": "success",
        "action": "info",
        "skill": "frontend-slides",
        "description": "Template-based HTML presentation generator with 34 curated visual themes",
        "template_count": len(index.get("templates", [])),
    }, ensure_ascii=False))

def do_list_templates(args):
    index = load_index()
    templates = []
    for t in index.get("templates", []):
        templates.append({
            "slug": t["slug"],
            "name": t["name"],
            "tagline": t["tagline"],
            "mood": t.get("mood", []),
            "occasion": t.get("occasion", []),
            "scheme": t.get("scheme", ""),
            "slide_count": t.get("slide_count", 0),
        })
    print(json.dumps({"status": "success", "action": "list_templates", "templates": templates}, ensure_ascii=False))

def do_match_templates(args):
    keywords = [k.lower() for k in args.get("keywords", [])]
    mood = args.get("mood", "").lower()
    occasion = args.get("occasion", "").lower()
    scheme = args.get("scheme", "").lower()

    index = load_index()
    scored = []
    for t in index.get("templates", []):
        score = 0
        text = " ".join([
            " ".join(t.get("mood", [])),
            " ".join(t.get("occasion", [])),
            " ".join(t.get("tone", [])),
            t.get("best_for", ""),
            t.get("tagline", ""),
        ]).lower()
        for kw in keywords:
            if kw in text:
                score += 2
        if mood and mood in text:
            score += 3
        if occasion and occasion in text:
            score += 3
        if scheme and t.get("scheme", "") == scheme:
            score += 2
        scored.append((score, t))

    scored.sort(key=lambda x: -x[0])
    results = []
    for score, t in scored[:5]:
        results.append({
            "slug": t["slug"],
            "name": t["name"],
            "tagline": t["tagline"],
            "mood": t.get("mood", []),
            "scheme": t.get("scheme", ""),
            "slide_count": t.get("slide_count", 0),
            "best_for": t.get("best_for", "")[:120],
            "match_score": score,
        })
    print(json.dumps({"status": "success", "action": "match_templates", "matches": results}, ensure_ascii=False))

def do_read_template(args):
    slug = args.get("slug", "")
    if not slug:
        print(json.dumps({"status": "error", "msg": "slug is required"}, ensure_ascii=False))
        sys.exit(1)

    template_dir = os.path.join(TEMPLATES_DIR, slug)
    if not os.path.isdir(template_dir):
        print(json.dumps({"status": "error", "msg": f"Template not found: {slug}"}, ensure_ascii=False))
        sys.exit(1)

    html_path = os.path.join(template_dir, "template.html")
    json_path = os.path.join(template_dir, "template.json")

    result = {"status": "success", "action": "read_template", "slug": slug}
    if os.path.exists(json_path):
        with open(json_path, "r", encoding="utf-8") as f:
            result["meta"] = json.load(f)
    if os.path.exists(html_path):
        with open(html_path, "r", encoding="utf-8") as f:
            result["html"] = f.read()
    siblings = [f for f in os.listdir(template_dir) if f not in ("template.html", "template.json")]
    result["sibling_files"] = siblings
    print(json.dumps(result, ensure_ascii=False))

def do_read_file(args):
    filename = args.get("file", "SKILL.md")
    filepath = os.path.join(SKILL_DIR, filename)
    if not os.path.exists(filepath):
        print(json.dumps({"status": "error", "msg": f"File not found: {filename}"}, ensure_ascii=False))
        sys.exit(1)
    with open(filepath, "r", encoding="utf-8") as f:
        content = f.read()
    print(json.dumps({"status": "success", "action": "read_file", "file": filename, "content": content}, ensure_ascii=False))

def extract_slide_blocks(html):
    blocks = []
    pattern = re.compile(r'<div\s+class="slide\s+layout-(\w+)[^"]*"[^>]*>')
    for m in pattern.finditer(html):
        layout = m.group(1)
        start = m.start()
        depth = 0
        pos = m.end()
        while pos < len(html):
            open_match = re.search(r'<div[\s>]', html[pos:])
            close_match = re.search(r'</div>', html[pos:])
            if not close_match:
                break
            if open_match and open_match.start() < close_match.start():
                depth += 1
                pos += open_match.end()
            else:
                if depth == 0:
                    end = pos + close_match.end()
                    blocks.append((layout, html[start:end]))
                    break
                depth -= 1
                pos += close_match.end()
    return blocks

def fill_slide(slide_html, heading, body, items):
    if heading:
        slide_html = re.sub(r'(<h1[^>]*>)(.*?)(</h1>)', rf'\g<1>{esc(heading)}\3', slide_html, count=1, flags=re.DOTALL)
        if heading not in slide_html:
            slide_html = re.sub(r'(<h2[^>]*>)(.*?)(</h2>)', rf'\g<1>{esc(heading)}\3', slide_html, count=1, flags=re.DOTALL)
    if body:
        slide_html = re.sub(
            r'(<p[^>]*class="[^"]*subtitle[^"]*"[^>]*>)(.*?)(</p>)',
            rf'\g<1>{esc(body)}\3', slide_html, count=1, flags=re.DOTALL
        )
        if body not in slide_html:
            slide_html = re.sub(
                r'(<p[^>]*class="[^"]*closing-sub[^"]*"[^>]*>)(.*?)(</p>)',
                rf'\g<1>{esc(body)}\3', slide_html, count=1, flags=re.DOTALL
            )
        if body not in slide_html:
            slide_html = re.sub(
                r'(<p[^>]*class="[^"]*quote-source[^"]*"[^>]*>)(.*?)(</p>)',
                rf'\g<1>{esc(body)}\3', slide_html, count=1, flags=re.DOTALL
            )
    if items:
        if 'layout-metrics' in slide_html:
            slide_html = fill_metrics_cards(slide_html, items)
        elif 'layout-dashboard' in slide_html:
            slide_html = fill_dashboard_cells(slide_html, items)
        elif 'layout-bars' in slide_html:
            slide_html = fill_bars(slide_html, items)
        elif 'layout-timeline' in slide_html:
            slide_html = fill_timeline(slide_html, items)
        elif 'layout-agenda' in slide_html:
            slide_html = fill_agenda(slide_html, items)
        elif 'layout-detail' in slide_html:
            slide_html = fill_detail(slide_html, items)
        else:
            slide_html = fill_li_items(slide_html, items)
    return slide_html

def extract_inner_blocks(html, class_name):
    blocks = []
    pattern = re.compile(rf'<div\s+class="{re.escape(class_name)}"[^>]*>')
    for m in pattern.finditer(html):
        start = m.start()
        depth = 0
        pos = m.end()
        while pos < len(html):
            open_match = re.search(r'<div[\s>]', html[pos:])
            close_match = re.search(r'</div>', html[pos:])
            if not close_match:
                break
            if open_match and open_match.start() < close_match.start():
                depth += 1
                pos += open_match.end()
            else:
                if depth == 0:
                    end = pos + close_match.end()
                    inner_start = m.end()
                    inner_end = pos
                    blocks.append((html[start:end], html[inner_start:inner_end]))
                    break
                depth -= 1
                pos += close_match.end()
    return blocks

def fill_metrics_cards(slide_html, items):
    cards = extract_inner_blocks(slide_html, "metric-card")
    if not cards:
        return fill_li_items(slide_html, items)
    for i, (full_html, inner_html) in enumerate(cards):
        if i >= len(items):
            break
        item = items[i]
        new_inner = inner_html
        if isinstance(item, dict):
            if "value" in item:
                new_inner = re.sub(r'(<div class="metric-value">)(.*?)(</div>)', rf'\g<1>{esc(str(item["value"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "label" in item:
                new_inner = re.sub(r'(<div class="metric-label">)(.*?)(</div>)', rf'\g<1>{esc(str(item["label"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "desc" in item:
                new_inner = re.sub(r'(<div class="metric-desc">)(.*?)(</div>)', rf'\g<1>{esc(str(item["desc"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "items" in item:
                sub_items = item["items"]
                li_pattern = re.compile(r'(<li[^>]*>)(.*?)(</li>)', re.DOTALL)
                li_matches = list(li_pattern.finditer(new_inner))
                for j, sub in enumerate(sub_items):
                    if j < len(li_matches):
                        new_inner = new_inner.replace(li_matches[j].group(0), f'<li>{esc(str(sub))}</li>', 1)
        else:
            new_inner = re.sub(r'(<div class="metric-label">)(.*?)(</div>)', rf'\g<1>{esc(str(item))}\3', new_inner, count=1, flags=re.DOTALL)
            new_inner = re.sub(r'(<div class="metric-desc">)(.*?)(</div>)', rf'\g<1>\3', new_inner, count=1, flags=re.DOTALL)
            li_pattern = re.compile(r'(<li[^>]*>)(.*?)(</li>)', re.DOTALL)
            li_matches = list(li_pattern.finditer(new_inner))
            for j in range(len(li_matches)):
                new_inner = new_inner.replace(li_matches[j].group(0), '', 1)
            new_inner = re.sub(r'<ul class="metric-supports">\s*</ul>', '', new_inner)
            new_inner = re.sub(r'<div class="metric-change[^"]*">.*?</div>', '', new_inner, flags=re.DOTALL)
        slide_html = slide_html.replace(inner_html, new_inner, 1)
    return slide_html

def fill_dashboard_cells(slide_html, items):
    cells = extract_inner_blocks(slide_html, "stat-cell")
    for i, (full_html, inner_html) in enumerate(cells):
        if i >= len(items):
            break
        item = items[i]
        new_inner = inner_html
        if isinstance(item, dict):
            if "value" in item:
                new_inner = re.sub(r'(<span class="stat-num">)(.*?)(</span>)', rf'\g<1>{esc(str(item["value"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "label" in item:
                new_inner = re.sub(r'(<div class="stat-name">)(.*?)(</div>)', rf'\g<1>{esc(str(item["label"]))}\3', new_inner, count=1, flags=re.DOTALL)
        else:
            new_inner = re.sub(r'(<div class="stat-name">)(.*?)(</div>)', rf'\g<1>{esc(str(item))}\3', new_inner, count=1, flags=re.DOTALL)
        slide_html = slide_html.replace(inner_html, new_inner, 1)
    return slide_html

def fill_bars(slide_html, items):
    bars = extract_inner_blocks(slide_html, "bar-item")
    for i, (full_html, inner_html) in enumerate(bars):
        if i >= len(items):
            break
        item = items[i]
        new_inner = inner_html
        if isinstance(item, dict):
            if "label" in item:
                new_inner = re.sub(r'(<div class="bar-label">)(.*?)(</div>)', rf'\g<1>{esc(str(item["label"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "value" in item:
                pct = str(item["value"]).replace('%','')
                new_inner = re.sub(r'style="width: \d+%"', f'style="width: {pct}%"', new_inner, count=1)
                new_inner = re.sub(r'(<div class="bar-pct">)(.*?)(</div>)', rf'\g<1>{esc(str(item["value"]))}\3', new_inner, count=1, flags=re.DOTALL)
        else:
            new_inner = re.sub(r'(<div class="bar-label">)(.*?)(</div>)', rf'\g<1>{esc(str(item))}\3', new_inner, count=1, flags=re.DOTALL)
        slide_html = slide_html.replace(inner_html, new_inner, 1)
    return slide_html

def fill_timeline(slide_html, items):
    steps = extract_inner_blocks(slide_html, "timeline-step")
    for i, (full_html, inner_html) in enumerate(steps):
        if i >= len(items):
            break
        item = items[i]
        new_inner = inner_html
        if isinstance(item, dict):
            if "title" in item:
                new_inner = re.sub(r'(<div class="step-title">)(.*?)(</div>)', rf'\g<1>{esc(str(item["title"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "desc" in item:
                new_inner = re.sub(r'(<div class="step-desc">)(.*?)(</div>)', rf'\g<1>{esc(str(item["desc"]))}\3', new_inner, count=1, flags=re.DOTALL)
        else:
            new_inner = re.sub(r'(<div class="step-title">)(.*?)(</div>)', rf'\g<1>{esc(str(item))}\3', new_inner, count=1, flags=re.DOTALL)
            new_inner = re.sub(r'(<div class="step-desc">)(.*?)(</div>)', rf'\g<1>\3', new_inner, count=1, flags=re.DOTALL)
        slide_html = slide_html.replace(inner_html, new_inner, 1)
    return slide_html

def fill_agenda(slide_html, items):
    agenda_items = extract_inner_blocks(slide_html, "agenda-item")
    for i, (full_html, inner_html) in enumerate(agenda_items):
        if i >= len(items):
            break
        item = items[i]
        new_inner = inner_html
        if isinstance(item, dict):
            if "title" in item:
                new_inner = re.sub(r'(<h3>)(.*?)(</h3>)', rf'\g<1>{esc(str(item["title"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "desc" in item:
                new_inner = re.sub(r'(<p>)(.*?)(</p>)', rf'\g<1>{esc(str(item["desc"]))}\3', new_inner, count=1, flags=re.DOTALL)
        else:
            new_inner = re.sub(r'(<h3>)(.*?)(</h3>)', rf'\g<1>{esc(str(item))}\3', new_inner, count=1, flags=re.DOTALL)
            new_inner = re.sub(r'(<p>)(.*?)(</p>)', rf'\g<1>\3', new_inner, count=1, flags=re.DOTALL)
        slide_html = slide_html.replace(inner_html, new_inner, 1)
    return slide_html

def fill_detail(slide_html, items):
    detail_blocks = extract_inner_blocks(slide_html, "detail-block")
    for i, (full_html, inner_html) in enumerate(detail_blocks):
        if i >= len(items):
            break
        item = items[i]
        new_inner = inner_html
        if isinstance(item, dict):
            if "title" in item:
                new_inner = re.sub(r'(<h3>)(.*?)(</h3>)', rf'\g<1>{esc(str(item["title"]))}\3', new_inner, count=1, flags=re.DOTALL)
            if "items" in item:
                sub_items = item["items"]
                li_pattern = re.compile(r'(<li>)(.*?)(</li>)', re.DOTALL)
                li_matches = list(li_pattern.finditer(new_inner))
                for j, sub in enumerate(sub_items):
                    if j < len(li_matches):
                        new_inner = new_inner.replace(li_matches[j].group(0), f'<li>{esc(str(sub))}</li>', 1)
        else:
            new_inner = re.sub(r'(<h3>)(.*?)(</h3>)', rf'\g<1>{esc(str(item))}\3', new_inner, count=1, flags=re.DOTALL)
        slide_html = slide_html.replace(inner_html, new_inner, 1)
    return slide_html

def fill_li_items(slide_html, items):
    li_pattern = re.compile(r'(<li[^>]*>)(.*?)(</li>)', re.DOTALL)
    li_matches = list(li_pattern.finditer(slide_html))
    for j, item_text in enumerate(items):
        raw = item_text if isinstance(item_text, str) else item_text.get("text", str(item_text))
        if j < len(li_matches):
            old = li_matches[j].group(0)
            new = f'<li>{esc(raw)}</li>'
            slide_html = slide_html.replace(old, new, 1)
    return slide_html

def inline_scripts(html, template_dir):
    def replace_local_script(match):
        src = match.group(1)
        if src.startswith("http://") or src.startswith("https://") or src.startswith("//"):
            return match.group(0)
        js_path = os.path.join(template_dir, src)
        if os.path.isfile(js_path):
            with open(js_path, "r", encoding="utf-8") as f:
                js_content = f.read()
            return f"<script>\n{js_content}\n</script>"
        return match.group(0)

    html = re.sub(
        r'<script\s+src="([^"]+)"\s*>\s*</script>',
        replace_local_script,
        html
    )
    html = re.sub(
        r"<script\s+src='([^']+)'\s*>\s*</script>",
        replace_local_script,
        html
    )
    return html

def do_generate(args):
    slug = args.get("slug", "")
    if not slug:
        print(json.dumps({"status": "error", "msg": "slug is required"}, ensure_ascii=False))
        sys.exit(1)

    template_dir = os.path.join(TEMPLATES_DIR, slug)
    if not os.path.isdir(template_dir):
        print(json.dumps({"status": "error", "msg": f"Template not found: {slug}"}, ensure_ascii=False))
        sys.exit(1)

    output_path = args.get("output_path", "")
    if not output_path:
        safe_name = re.sub(r'[^a-zA-Z0-9_\u4e00-\u9fff-]', '_', args.get("title", slug))
        output_path = os.path.join(os.getcwd(), f"{safe_name}.html")

    output_dir = os.path.dirname(os.path.abspath(output_path))
    os.makedirs(output_dir, exist_ok=True)

    html_path = os.path.join(template_dir, "template.html")
    if not os.path.exists(html_path):
        print(json.dumps({"status": "error", "msg": "template.html not found"}, ensure_ascii=False))
        sys.exit(1)

    with open(html_path, "r", encoding="utf-8") as f:
        html = f.read()

    title = args.get("title", "")
    subtitle = args.get("subtitle", "")
    author = args.get("author", "")
    date_str = args.get("date", "")
    replacements = args.get("replacements", {})
    slides = args.get("slides", [])

    if slides:
        html = rebuild_with_slides(html, slides, title, subtitle, author, date_str)
    else:
        if title:
            html = re.sub(r'<title>.*?</title>', f'<title>{esc(title)}</title>', html, count=1)
            html = re.sub(r'(<h1[^>]*>)(.*?)(</h1>)', rf'\g<1>{esc(title)}\3', html, count=1, flags=re.DOTALL)
        if subtitle:
            html = re.sub(
                r'(<p[^>]*class="[^"]*subtitle[^"]*"[^>]*>)(.*?)(</p>)',
                rf'\g<1>{esc(subtitle)}\3', html, count=1, flags=re.DOTALL
            )
        if author:
            for ph in ["Author Name", "Your Name", "[Author]"]:
                if ph in html:
                    html = html.replace(ph, author, 1)
                    break
        if date_str:
            for ph in ["May 2025", "January 2025", "2025", "2024", "[Date]"]:
                if ph in html:
                    html = html.replace(ph, date_str, 1)
                    break

    for old_text, new_text in replacements.items():
        html = html.replace(old_text, new_text)

    html = inline_scripts(html, template_dir)

    with open(output_path, "w", encoding="utf-8") as f:
        f.write(html)

    slide_count = len(slides) if slides else html.count('class="slide ')
    print(json.dumps({
        "status": "success",
        "action": "generate",
        "output_path": os.path.abspath(output_path),
        "template": slug,
        "slide_count": slide_count,
    }, ensure_ascii=False))

def rebuild_with_slides(html, slides, title, subtitle, author, date_str):
    blocks = extract_slide_blocks(html)
    if not blocks:
        return html

    layout_map = {}
    for layout, block_html in blocks:
        if layout not in layout_map:
            layout_map[layout] = block_html

    first_block_start = html.find(blocks[0][1])
    last_block_html = blocks[-1][1]
    last_block_start = html.rfind(last_block_html)
    last_block_end = last_block_start + len(last_block_html)

    deck_close_pos = find_deck_close(html, last_block_end)

    before_deck = html[:first_block_start]
    after_deck = html[deck_close_pos:]

    new_slides = []
    for i, slide_data in enumerate(slides):
        layout = slide_data.get("layout", "")
        if not layout:
            if i == 0:
                layout = "cover"
            elif i == len(slides) - 1:
                layout = "closing"
            else:
                layout = "metrics"

        template_html = layout_map.get(layout, blocks[min(i, len(blocks) - 1)][1])

        template_html = re.sub(r'class="slide\s+layout-\w+[^"]*"', f'class="slide layout-{layout}"', template_html, count=1)
        if i == 0:
            template_html = template_html.replace('class="slide layout-cover"', 'class="slide layout-cover active"')

        heading = slide_data.get("heading", "")
        body = slide_data.get("body", "")
        items = slide_data.get("items", [])

        if i == 0 and title:
            heading = heading or title
        if i == 0 and subtitle:
            body = body or subtitle

        template_html = fill_slide(template_html, heading, body, items)

        if author and i == 0:
            for ph in ["Author Name", "Your Name", "[Author]"]:
                if ph in template_html:
                    template_html = template_html.replace(ph, author, 1)
                    break
        if date_str and i == 0:
            for ph in ["Q2 2026", "Q1 2026", "May 2025", "January 2025", "2025", "2024", "[Date]"]:
                if ph in template_html:
                    template_html = template_html.replace(ph, date_str, 1)
                    break

        new_slides.append(template_html)

    slides_html = "\n\n".join(new_slides)
    result = before_deck + slides_html + "\n\n" + after_deck

    total = len(slides)
    result = re.sub(r'<span id="total">\d+</span>', f'<span id="total">{total}</span>', result)

    if title:
        result = re.sub(r'<title>.*?</title>', f'<title>{esc(title)}</title>', result, count=1)

    return result

def find_deck_close(html, search_from):
    depth = 0
    pos = search_from
    in_deck = False
    while pos < len(html):
        open_m = re.search(r'<div[\s>]', html[pos:])
        close_m = re.search(r'</div>', html[pos:])
        if not close_m:
            return len(html)
        if open_m and open_m.start() < close_m.start():
            depth += 1
            pos += open_m.end()
        else:
            if depth == 0:
                return pos + close_m.end()
            depth -= 1
            pos += close_m.end()
    return len(html)

def esc(text):
    return text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace('"', "&quot;")

def do_extract_pptx(args):
    pptx_path = args.get("pptx_path", "")
    if not pptx_path:
        print(json.dumps({"status": "error", "msg": "pptx_path is required"}, ensure_ascii=False))
        sys.exit(1)
    script_path = os.path.join(SKILL_DIR, "scripts", "extract-pptx.py")
    if not os.path.exists(script_path):
        print(json.dumps({"status": "error", "msg": "extract-pptx.py not found"}, ensure_ascii=False))
        sys.exit(1)
    cmd = [sys.executable, script_path, pptx_path]
    output_dir = args.get("output_dir", "")
    if output_dir:
        cmd.append(output_dir)
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
    print(json.dumps({
        "status": "success" if result.returncode == 0 else "error",
        "action": "extract_pptx",
        "stdout": result.stdout,
        "stderr": result.stderr,
        "exit_code": result.returncode,
    }, ensure_ascii=False))

def do_list_files(args):
    files = []
    for root, dirs, filenames in os.walk(SKILL_DIR):
        for f in filenames:
            relpath = os.path.relpath(os.path.join(root, f), SKILL_DIR)
            files.append(relpath.replace("\\", "/"))
    print(json.dumps({"status": "success", "action": "list_files", "files": sorted(files)}, ensure_ascii=False))

def load_index():
    index_path = os.path.join(SKILL_DIR, "index.json")
    if not os.path.exists(index_path):
        return {"templates": []}
    with open(index_path, "r", encoding="utf-8") as f:
        return json.load(f)

if __name__ == "__main__":
    main()
