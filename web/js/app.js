const API = '';
let token = localStorage.getItem('token');
let user = null;
let ws = null;
let isRunning = false;

try { user = JSON.parse(localStorage.getItem('user') || 'null'); } catch(e) {}

if (!token) { window.location.href = '/login'; }

function init() {
  if (user) {
    document.getElementById('userName').textContent = user.name || user.email;
    document.getElementById('userEmail').textContent = user.email;
    document.getElementById('userAvatar').textContent = (user.name || user.email)[0].toUpperCase();
  }
  loadChatHistory();
  loadFiles();
  loadSkills();
}

function switchView(view, el) {
  document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
  document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
  document.getElementById('view-' + view).classList.add('active');
  if (el) el.classList.add('active');
}

function toggleUserMenu() {
  document.getElementById('userMenu').classList.toggle('show');
}

document.addEventListener('click', (e) => {
  const menu = document.getElementById('userMenu');
  if (!e.target.closest('.sidebar-footer')) menu.classList.remove('show');
});

function handleLogout() {
  localStorage.removeItem('token');
  localStorage.removeItem('user');
  window.location.href = '/login';
}

function handleInputKey(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendMessage();
  }
}

function autoResize(el) {
  el.style.height = 'auto';
  el.style.height = Math.min(el.scrollHeight, 120) + 'px';
}

document.getElementById('chatInput').addEventListener('input', function() {
  autoResize(this);
});

async function sendMessage() {
  const input = document.getElementById('chatInput');
  const text = input.value.trim();
  if (!text || isRunning) return;

  input.value = '';
  input.style.height = 'auto';
  isRunning = true;
  updateStatus('running');

  appendMessage('user', text);

  const agentMsg = appendMessage('agent', '');
  const bubble = agentMsg.querySelector('.msg-bubble');
  const typingEl = createTypingIndicator();
  bubble.appendChild(typingEl);

  let fullContent = '';

  try {
    const r = await fetch(API + '/api/agent/stream', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token
      },
      body: JSON.stringify({ prompt: text })
    });

    if (r.status === 401) { handleLogout(); return; }

    const reader = r.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() || '';

      for (const line of lines) {
        if (!line.startsWith('data: ')) continue;
        const jsonStr = line.slice(6).trim();
        if (!jsonStr) continue;

        let d;
        try { d = JSON.parse(jsonStr); } catch(e) { continue; }

        if (d.source === 'final' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          fullContent += d.content;
          bubble.innerHTML = renderMarkdown(fullContent);
          const container = document.getElementById('chatMessages');
          container.scrollTop = container.scrollHeight;
        } else if (d.source === 'tool' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
        } else if (d.source === 'error' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          fullContent += d.content;
          bubble.innerHTML = renderMarkdown(fullContent);
        } else if (d.done) {
          if (typingEl.parentNode) typingEl.remove();
          if (!fullContent) {
            bubble.innerHTML = renderMarkdown('Task completed.');
          }
        }
      }
    }
  } catch (err) {
    typingEl.remove();
    bubble.textContent = 'Network error: ' + err.message;
  }

  isRunning = false;
  updateStatus('ready');
  loadFiles();
}

async function loadChatHistory() {
  try {
    const r = await fetch(API + '/api/chat/history', {
      headers: { 'Authorization': 'Bearer ' + token }
    });
    if (r.ok) {
      const d = await r.json();
      const messages = d.messages || [];
      const container = document.getElementById('chatMessages');
      const empty = container.querySelector('.empty-state');
      if (empty) empty.remove();
      messages.forEach(m => {
        appendMessage(m.role, m.content);
      });
      if (messages.length === 0) {
        container.innerHTML = '<div class="empty-state"><svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 015.83 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg><p>Describe your task and the agent will execute it autonomously.</p></div>';
      }
    }
  } catch(e) {}
}

function appendMessage(role, content) {
  const container = document.getElementById('chatMessages');
  const empty = container.querySelector('.empty-state');
  if (empty) empty.remove();

  const msg = document.createElement('div');
  msg.className = 'msg msg-' + role;

  const label = document.createElement('div');
  label.className = 'msg-label';
  label.textContent = role === 'user' ? 'You' : 'Agent';

  const bubble = document.createElement('div');
  bubble.className = 'msg-bubble';
  if (content) bubble.innerHTML = renderMarkdown(content);

  msg.appendChild(label);
  msg.appendChild(bubble);
  container.appendChild(msg);
  container.scrollTop = container.scrollHeight;
  return msg;
}

function createTypingIndicator() {
  const el = document.createElement('div');
  el.className = 'typing-indicator';
  el.innerHTML = '<span></span><span></span><span></span>';
  return el;
}

function updateStatus(state) {
  const badge = document.getElementById('agentStatus');
  badge.className = 'status-badge ' + state;
  badge.textContent = state === 'running' ? 'Running' : state === 'error' ? 'Error' : 'Ready';
}

function renderMarkdown(text) {
  let html = text
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, function(m, lang, code) {
    return '<pre><code class="lang-' + lang + '">' + code.trim() + '</code></pre>';
  });

  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');

  html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
  html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
  html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
  html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');

  html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
  html = html.replace(/\*(.+?)\*/g, '<em>$1</em>');

  html = html.replace(/^---+$/gm, '<hr>');

  html = html.replace(/^\|(.+)\|$/gm, function(m, row) {
    var cells = row.split('|').map(function(c) { return '<td>' + c.trim() + '</td>'; }).join('');
    return '<tr>' + cells + '</tr>';
  });
  html = html.replace(/(<tr>[\s\S]*?<\/tr>)/g, function(m) {
    if (m.indexOf('<table') === -1) return '<table>' + m + '</table>';
    return m;
  });
  html = html.replace(/<\/table>\s*<table>/g, '');

  html = html.replace(/^&gt; (.+)$/gm, '<blockquote>$1</blockquote>');
  html = html.replace(/<\/blockquote>\s*<blockquote>/g, '<br>');

  html = html.replace(/^[-*] (.+)$/gm, '<li>$1</li>');
  html = html.replace(/(<li>[\s\S]*?<\/li>)/g, '<ul>$1</ul>');
  html = html.replace(/<\/ul>\s*<ul>/g, '');

  html = html.replace(/^\d+\. (.+)$/gm, '<li>$1</li>');

  html = html.replace(/\n{2,}/g, '</p><p>');
  html = html.replace(/\n/g, '<br>');
  html = '<p>' + html + '</p>';
  html = html.replace(/<p>\s*<\/p>/g, '');
  html = html.replace(/<p>\s*(<h[1-4]>)/g, '$1');
  html = html.replace(/(<\/h[1-4]>)\s*<\/p>/g, '$1');
  html = html.replace(/<p>\s*(<pre>)/g, '$1');
  html = html.replace(/(<\/pre>)\s*<\/p>/g, '$1');
  html = html.replace(/<p>\s*(<ul>)/g, '$1');
  html = html.replace(/(<\/ul>)\s*<\/p>/g, '$1');
  html = html.replace(/<p>\s*(<table>)/g, '$1');
  html = html.replace(/(<\/table>)\s*<\/p>/g, '$1');
  html = html.replace(/<p>\s*(<hr>)/g, '$1');
  html = html.replace(/<p>\s*(<blockquote>)/g, '$1');
  html = html.replace(/(<\/blockquote>)\s*<\/p>/g, '$1');

  return html;
}

async function loadFiles() {
  try {
    const r = await fetch(API + '/api/workspace/files', {
      headers: { 'Authorization': 'Bearer ' + token }
    });
    if (r.status === 401) { handleLogout(); return; }
    const d = await r.json();
    renderFiles(d.files || []);
  } catch(e) {}
}

function renderFiles(files) {
  const container = document.getElementById('fileList');
  if (!files.length) {
    container.innerHTML = '<div class="empty-state"><p>No files yet. Upload or let the agent create them.</p></div>';
    return;
  }
  container.innerHTML = files.map(f => {
    const ext = f.name.split('.').pop().toUpperCase().slice(0, 3);
    const size = f.size > 1024 ? (f.size / 1024).toFixed(1) + ' KB' : f.size + ' B';
    return `<div class="file-item">
      <div class="file-icon">${ext}</div>
      <div class="file-info">
        <div class="file-name">${escHtml(f.name)}</div>
        <div class="file-meta">${size} &middot; ${f.mod_time}</div>
      </div>
      <div class="file-actions">
        <button onclick="downloadFile('${escAttr(f.path)}')">Download</button>
        <button class="del" onclick="deleteFile('${escAttr(f.path)}')">Delete</button>
      </div>
    </div>`;
  }).join('');
}

async function handleUpload(input) {
  const file = input.files[0];
  if (!file) return;
  const fd = new FormData();
  fd.append('file', file);
  try {
    const r = await fetch(API + '/api/workspace/upload', {
      method: 'POST',
      headers: { 'Authorization': 'Bearer ' + token },
      body: fd
    });
    if (r.ok) loadFiles();
  } catch(e) {}
  input.value = '';
}

function downloadFile(path) {
  window.open(API + '/api/workspace/download?path=' + encodeURIComponent(path) + '&token=' + token);
}

async function deleteFile(path) {
  if (!confirm('Delete ' + path + '?')) return;
  try {
    await fetch(API + '/api/workspace/file?path=' + encodeURIComponent(path), {
      method: 'DELETE',
      headers: { 'Authorization': 'Bearer ' + token }
    });
    loadFiles();
  } catch(e) {}
}

async function loadSkills() {
  try {
    const r = await fetch(API + '/api/skills', {
      headers: { 'Authorization': 'Bearer ' + token }
    });
    if (r.ok) {
      const d = await r.json();
      renderSkills(d.skills || []);
      return;
    }
  } catch(e) {}

  const container = document.getElementById('skillList');
  container.innerHTML = '<div class="empty-state"><p>Failed to load skills.</p></div>';
}

function renderSkills(skills) {
  const container = document.getElementById('skillList');
  if (!skills.length) {
    container.innerHTML = '<div class="empty-state"><p>No skills available.</p></div>';
    return;
  }
  container.innerHTML = skills.map(s => {
    const icon = s.name[0].toUpperCase();
    const desc = s.description ? escHtml(s.description) : 'No description';
    let html = `<div class="skill-card">
      <div class="skill-card-header" onclick="toggleSkillDetail(this)">
        <div class="skill-icon">${icon}</div>
        <div class="skill-info">
          <div class="skill-name">${escHtml(s.name)}</div>
          <div class="skill-desc">${desc}</div>
        </div>
        <svg class="skill-chevron" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="6 9 12 15 18 9"/></svg>
      </div>`;

    if (s.templates && s.templates.length) {
      html += `<div class="skill-templates">
        <div class="templates-label">${s.templates.length} templates available</div>
        <div class="template-grid">
          ${s.templates.map(t => {
            const scheme = t.scheme === 'dark' ? '🌙' : t.scheme === 'mixed' ? '🌓' : '☀️';
            const formality = t.formality || '';
            return `<div class="template-item" title="${escHtml(t.tagline || '')}">
              <div class="template-name">${escHtml(t.name)}</div>
              <div class="template-meta">${scheme} ${formality}</div>
            </div>`;
          }).join('')}
        </div>
      </div>`;
    }

    html += '</div>';
    return html;
  }).join('');
}

function toggleSkillDetail(el) {
  const card = el.closest('.skill-card');
  card.classList.toggle('expanded');
}

function escHtml(s) {
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

function escAttr(s) {
  return s.replace(/'/g, "\\'").replace(/"/g, '&quot;');
}

init();
