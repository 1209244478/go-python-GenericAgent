const API = '';
let token = localStorage.getItem('token');
let user = null;
let ws = null;
let isRunning = false;
let currentSessionId = 0;

try { user = JSON.parse(localStorage.getItem('user') || 'null'); } catch(e) {}

if (!token) { window.location.href = '/login'; }

function initTheme() {
  const saved = localStorage.getItem('theme');
  if (saved === 'light') {
    document.documentElement.setAttribute('data-theme', 'light');
    document.getElementById('themeIconDark').style.display = 'none';
    document.getElementById('themeIconLight').style.display = 'block';
  }
}

function toggleTheme() {
  const isLight = document.documentElement.getAttribute('data-theme') === 'light';
  if (isLight) {
    document.documentElement.removeAttribute('data-theme');
    localStorage.setItem('theme', 'dark');
    document.getElementById('themeIconDark').style.display = 'block';
    document.getElementById('themeIconLight').style.display = 'none';
  } else {
    document.documentElement.setAttribute('data-theme', 'light');
    localStorage.setItem('theme', 'light');
    document.getElementById('themeIconDark').style.display = 'none';
    document.getElementById('themeIconLight').style.display = 'block';
  }
}

function openMobileSidebar() {
  document.getElementById('sidebar').classList.add('open');
  document.getElementById('mobileOverlay').classList.add('show');
}

function closeMobileSidebar() {
  document.getElementById('sidebar').classList.remove('open');
  document.getElementById('mobileOverlay').classList.remove('show');
}

function init() {
  initTheme();
  if (user) {
    document.getElementById('userName').textContent = user.name || user.email;
    document.getElementById('userEmail').textContent = user.email;
    document.getElementById('userAvatar').textContent = (user.name || user.email)[0].toUpperCase();
  }
  loadSessions();
  loadFiles();
  loadSkills();
}

function switchView(view, el) {
  document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
  document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
  document.getElementById('view-' + view).classList.add('active');
  if (el) el.classList.add('active');
  closeMobileSidebar();
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
  if (isRunning) return;
  var input = document.getElementById('chatInput');
  var text = input.value.trim();
  if (!text) return;
  input.value = '';
  input.style.height = 'auto';
  isRunning = true;
  updateStatus('running');

  appendMessage('user', text);

  var agentMsg = appendMessage('agent', '');
  var bubble = agentMsg.querySelector('.msg-bubble');
  var typingEl = createTypingIndicator();
  bubble.appendChild(typingEl);

  var fullContent = '';

  try {
    var r = await fetch(API + '/api/agent/stream', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token
      },
      body: JSON.stringify({ prompt: text, session_id: currentSessionId })
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
    var url = API + '/api/chat/history';
    if (currentSessionId > 0) url += '?session_id=' + currentSessionId;
    const r = await fetch(url, {
      headers: { 'Authorization': 'Bearer ' + token }
    });
    if (r.ok) {
      const d = await r.json();
      const messages = d.messages || [];
      const container = document.getElementById('chatMessages');
      container.innerHTML = '';
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

  html = html.replace(/```interactive\n([\s\S]*?)```/g, function(m, jsonStr) {
    try {
      var cfg = JSON.parse(jsonStr.trim());
      return renderInteractiveCard(cfg);
    } catch(e) {
      return '<pre><code>' + jsonStr.trim() + '</code></pre>';
    }
  });

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
  html = html.replace(/<p>\s*(<div class="interactive-card")/g, '$1');
  html = html.replace(/(<\/div>)\s*<\/p>/g, '$1');

  return html;
}

var _interactiveCardId = 0;

function renderInteractiveCard(cfg) {
  var cardId = 'icard_' + (++_interactiveCardId) + '_' + (cfg.id || 'unknown');
  if (cfg.type === 'input') {
    return '<div class="interactive-card" id="' + cardId + '">' +
      '<div class="icard-question">' + escHtml(cfg.question || '') + '</div>' +
      '<div class="icard-input-row">' +
        '<input type="text" class="icard-input" id="' + cardId + '_input" placeholder="' + escHtml(cfg.placeholder || '') + '">' +
        '<button class="icard-btn" onclick="submitInteractiveInput(\'' + cardId + '\',\'' + escAttr(cfg.id || '') + '\')">Submit</button>' +
      '</div>' +
    '</div>';
  }
  if (cfg.type === 'choice') {
    var opts = (cfg.options || []).map(function(opt, i) {
      return '<button class="icard-choice-btn" onclick="submitInteractiveChoice(\'' + cardId + '\',\'' + escAttr(cfg.id || '') + '\',\'' + escAttr(opt) + '\')">' + escHtml(opt) + '</button>';
    }).join('');
    return '<div class="interactive-card" id="' + cardId + '">' +
      '<div class="icard-question">' + escHtml(cfg.question || '') + '</div>' +
      '<div class="icard-choices">' + opts + '</div>' +
    '</div>';
  }
  return '<pre><code>' + escHtml(JSON.stringify(cfg)) + '</code></pre>';
}

function submitInteractiveInput(cardId, fieldId) {
  var input = document.getElementById(cardId + '_input');
  if (!input || !input.value.trim()) return;
  var value = input.value.trim();
  var card = document.getElementById(cardId);
  if (card) {
    card.innerHTML = '<div class="icard-answered"><span class="icard-label">' + escHtml(fieldId) + '</span> ' + escHtml(value) + '</div>';
  }
  sendMessageText('[用户输入] ' + fieldId + ': ' + value);
}

function submitInteractiveChoice(cardId, fieldId, value) {
  var card = document.getElementById(cardId);
  if (card) {
    card.innerHTML = '<div class="icard-answered"><span class="icard-label">' + escHtml(fieldId) + '</span> ' + escHtml(value) + '</div>';
  }
  sendMessageText('[用户选择] ' + fieldId + ': ' + value);
}

async function sendMessageText(text) {
  if (isRunning) return;
  isRunning = true;
  updateStatus('running');

  appendMessage('user', text);

  var agentMsg = appendMessage('agent', '');
  var bubble = agentMsg.querySelector('.msg-bubble');
  var typingEl = createTypingIndicator();
  bubble.appendChild(typingEl);

  var fullContent = '';

  try {
    var r = await fetch(API + '/api/agent/stream', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token
      },
      body: JSON.stringify({ prompt: text, session_id: currentSessionId })
    });

    if (r.status === 401) { handleLogout(); return; }

    var reader = r.body.getReader();
    var decoder = new TextDecoder();
    var buffer = '';

    while (true) {
      var result = await reader.read();
      if (result.done) break;

      buffer += decoder.decode(result.value, { stream: true });
      var lines = buffer.split('\n');
      buffer = lines.pop() || '';

      for (var i = 0; i < lines.length; i++) {
        var line = lines[i];
        if (!line.startsWith('data: ')) continue;
        var jsonStr = line.slice(6).trim();
        if (!jsonStr) continue;

        var d;
        try { d = JSON.parse(jsonStr); } catch(e) { continue; }

        if (d.source === 'final' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          fullContent += d.content;
          bubble.innerHTML = renderMarkdown(fullContent);
          var container = document.getElementById('chatMessages');
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
        <button onclick="previewFile('${escAttr(f.path)}','${escAttr(f.name)}')">Preview</button>
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

function previewFile(path, name) {
  var ext = (name || '').split('.').pop().toLowerCase();
  var imageExts = ['png','jpg','jpeg','gif','webp','bmp','ico','svg'];
  var textExts = ['txt','md','json','yaml','yml','toml','ini','cfg','conf','env','csv','log','py','go','js','ts','tsx','jsx','vue','svelte','html','htm','css','scss','less','sh','bash','zsh','java','c','cpp','h','hpp','rs','rb','php','sql','xml'];
  var videoExts = ['mp4'];
  var audioExts = ['mp3','wav'];
  var pdfExts = ['pdf'];

  var previewUrl = API + '/api/workspace/preview?path=' + encodeURIComponent(path) + '&token=' + token;

  var modal = document.getElementById('previewModal');
  var title = document.getElementById('previewTitle');
  var body = document.getElementById('previewBody');

  title.textContent = name || path;

  if (imageExts.indexOf(ext) !== -1) {
    body.innerHTML = '<img src="' + previewUrl + '" style="max-width:100%;max-height:70vh;border-radius:8px" alt="' + escHtml(name) + '">';
  } else if (videoExts.indexOf(ext) !== -1) {
    body.innerHTML = '<video src="' + previewUrl + '" controls style="max-width:100%;max-height:70vh;border-radius:8px"></video>';
  } else if (audioExts.indexOf(ext) !== -1) {
    body.innerHTML = '<div style="padding:2rem;text-align:center"><div style="font-size:3rem;margin-bottom:1rem">&#9835;</div><audio src="' + previewUrl + '" controls style="width:100%"></audio></div>';
  } else if (pdfExts.indexOf(ext) !== -1) {
    body.innerHTML = '<iframe src="' + previewUrl + '" style="width:100%;height:70vh;border:none;border-radius:8px"></iframe>';
  } else if (textExts.indexOf(ext) !== -1 || ext === '') {
    fetchTextPreview(previewUrl, name);
  } else {
    body.innerHTML = '<div style="padding:2rem;text-align:center;color:var(--text-3)"><p>Preview not available for this file type</p><p style="font-size:.8125rem">.' + escHtml(ext) + ' files can be downloaded instead</p></div>';
  }

  modal.classList.add('show');
}

async function fetchTextPreview(url, name) {
  var body = document.getElementById('previewBody');
  try {
    var r = await fetch(url);
    if (!r.ok) {
      body.innerHTML = '<div style="padding:2rem;color:var(--danger)">Failed to load file</div>';
      return;
    }
    var text = await r.text();
    var lines = text.split('\n');
    var maxLines = 500;
    var truncated = lines.length > maxLines;
    if (truncated) {
      text = lines.slice(0, maxLines).join('\n');
    }
    var escaped = text.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
    body.innerHTML = '<pre class="preview-code">' + escaped + (truncated ? '\n\n... (showing first ' + maxLines + ' of ' + lines.length + ' lines)' : '') + '</pre>';
  } catch(e) {
    body.innerHTML = '<div style="padding:2rem;color:var(--danger)">Error: ' + escHtml(e.message) + '</div>';
  }
}

function closePreview() {
  document.getElementById('previewModal').classList.remove('show');
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

async function loadSessions() {
  try {
    var r = await fetch(API + '/api/sessions', {
      headers: { 'Authorization': 'Bearer ' + token }
    });
    if (r.ok) {
      var d = await r.json();
      var sessions = d.sessions || [];
      var container = document.getElementById('sessionList');

      if (sessions.length === 0) {
        var cr = await fetch(API + '/api/sessions', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer ' + token
          },
          body: JSON.stringify({ name: 'default' })
        });
        if (cr.ok) {
          var cd = await cr.json();
          currentSessionId = cd.session.id;
        }
        loadSessions();
        return;
      }

      if (currentSessionId === 0) {
        currentSessionId = sessions[0].id;
      }

      container.innerHTML = sessions.map(function(s) {
        var isActive = s.id === currentSessionId;
        return '<div class="session-item' + (isActive ? ' active' : '') + '" data-id="' + s.id + '" onclick="switchSession(' + s.id + ')">'
          + '<span class="session-name">' + escHtml(s.name) + '</span>'
          + '<button class="session-del" onclick="event.stopPropagation();deleteSession(' + s.id + ')" title="Delete">'
          + '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>'
          + '</button>'
          + '</div>';
      }).join('');

      loadChatHistory();
    }
  } catch(e) {}
}

function switchSession(id) {
  currentSessionId = parseInt(id, 10);
  loadSessions();
}

async function createNewSession() {
  var name = prompt('Session name:', 'New Session');
  if (!name) return;
  try {
    var r = await fetch(API + '/api/sessions', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token
      },
      body: JSON.stringify({ name: name })
    });
    if (r.ok) {
      var d = await r.json();
      currentSessionId = d.session.id;
      loadSessions();
    }
  } catch(e) {}
}

async function deleteSession(id) {
  if (!confirm('Delete this session and its chat history?')) return;
  try {
    var r = await fetch(API + '/api/sessions?session_id=' + id, {
      method: 'DELETE',
      headers: { 'Authorization': 'Bearer ' + token }
    });
    if (r.ok) {
      if (id === currentSessionId) currentSessionId = 0;
      loadSessions();
    } else {
      var d = await r.json();
      alert(d.error || 'Failed to delete session');
    }
  } catch(e) {}
}

init();
