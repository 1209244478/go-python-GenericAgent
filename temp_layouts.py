import re
html = open(r'C:\Users\wangrongzhou\Documents\Git\GenericAgent\skills\frontend-slides\templates\8-bit-orbit\template.html', 'r', encoding='utf-8').read()
matches = re.findall(r'class="slide\s+layout-(\w+)"', html)
print('\n'.join(sorted(set(matches))))
