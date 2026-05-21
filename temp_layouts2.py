import re
html = open(r'C:\Users\wangrongzhou\Documents\Git\GenericAgent\skills\frontend-slides\templates\8-bit-orbit\template.html', 'r', encoding='utf-8').read()
# Find all lines with 'slide' class
for i, line in enumerate(html.split('\n'), 1):
    if 'class=\"slide' in line.lower() or \"class='slide\" in line.lower():
        print(f\"Line {i}: {line.strip()[:200]}\")
