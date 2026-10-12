# Builds a static demo of the MVP (no server): relative links + demo-api.js that fakes the API in localStorage.
import os, re, shutil, sys
SRC = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', '..', 'public')
OUT = sys.argv[1]
HERE = os.path.dirname(os.path.abspath(__file__))
shutil.rmtree(OUT, ignore_errors=True)
os.makedirs(OUT + '/assets'); os.makedirs(OUT + '/learn'); os.makedirs(OUT + '/app')

def rel(s):
    s = s.replace('/learn/course.js', 'learn/course.js').replace('/app/app.js', 'app/app.js')
    s = s.replace('/assets/site.css', 'assets/site.css').replace('/favicon.svg', 'favicon.svg')
    s = re.sub(r"""(['"])/(cases|learn|app|admin)(?=[?#'"])""", r'\1\2.html', s)
    s = re.sub(r'''href="/"''', 'href="index.html"', s)
    return s

BANNER = ('<div style="background:#1C1B19;color:#F6F3EC;font:13px/1.4 system-ui,sans-serif;padding:8px 14px;text-align:center">'
          'Демо-версия: данные хранятся только в этом браузере. Анкета «проверяется» сама через 8 секунд. Ключ модератора: <b>demo</b>. '
          '<button type="button" onclick="__demoReset()" style="margin-left:6px;font:inherit;color:#FFD23F;background:none;border:0;text-decoration:underline;cursor:pointer">сбросить</button></div>')

for name in ['index.html', 'cases.html', 'learn.html', 'app.html', 'admin.html']:
    s = rel(open(os.path.join(SRC, name), encoding='utf-8').read())
    s = s.replace('<meta charset="utf-8">', '<meta charset="utf-8">\n<script src="demo-api.js"></script>', 1)
    if name in ('app.html', 'admin.html'):
        s = re.sub(r'<body>', '<body>\n' + BANNER, s, count=1)
    open(os.path.join(OUT, name), 'w', encoding='utf-8').write(s)
for src, dst in [('assets/site.css', 'assets/site.css'), ('learn/course.js', 'learn/course.js'), ('favicon.svg', 'favicon.svg')]:
    shutil.copy(os.path.join(SRC, src), os.path.join(OUT, dst))
open(os.path.join(OUT, 'app/app.js'), 'w', encoding='utf-8').write(rel(open(os.path.join(SRC, 'app/app.js'), encoding='utf-8').read()))
shutil.copy(os.path.join(HERE, 'demo-api.js'), os.path.join(OUT, 'demo-api.js'))
print('built', OUT)
