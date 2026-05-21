import re
html = open(r'C:\Users\wangrongzhou\Documents\Git\GenericAgent\slides_go_python.html', 'r', encoding='utf-8').read()

# Find layouts
slides = re.findall(r'class="slide\s+layout-(\w+)"', html)
print('=== Slide Layouts ===')
for i, s in enumerate(slides, 1):
    print(f'  Slide {i}: {s}')

# Find headings
headers = re.findall(r'<(h[12])[^>]*>(.*?)</\1>', html)
print('\n=== Headings ===')
for tag, text in headers:
    print(f'  {tag}: {text}')

# Find list items
items = re.findall(r'<li>(.*?)</li>', html)
print(f'\n=== List Items ({len(items)}) ===')
for it in items:
    print(f'  - {it}')

# Check body/subtitle
subs = re.findall(r'<p class="[^"]*subtitle[^"]*"[^>]*>(.*?)</p>', html)
print(f'\n=== Subtitle/Body ===')
for s in subs:
    print(f'  {s}')

# Title tag
t = re.findall(r'<title>(.*?)</title>', html)
print(f'\n=== Title tag ===')
for x in t:
    print(f'  {x}')
