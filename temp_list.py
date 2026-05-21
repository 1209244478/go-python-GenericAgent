import json
idx = json.load(open('C:/Users/wangrongzhou/Documents/Git/GenericAgent/skills/frontend-slides/index.json', 'r', encoding='utf-8'))
for t in idx['templates']:
    moods = ', '.join(t.get('mood', [])[:3])
    occasions = ', '.join(t.get('occasion', [])[:3])
    print(f"{t['slug']:25s} | {t['scheme']:7s} | {moods:40s} | {occasions}")
