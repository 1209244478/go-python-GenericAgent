const API = '';
let token = localStorage.getItem('token');
let user = null;
let ws = null;
let isRunning = false;
let abortController = null;
let currentTaskId = null;
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
  document.querySelectorAll('.nav-item-row').forEach(n => n.classList.remove('active'));
  document.getElementById('view-' + view).classList.add('active');
  if (el) {
    el.classList.add('active');
    var row = el.closest('.nav-item-row');
    if (row) row.classList.add('active');
  }
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
  setRunningUI(true);

  appendMessage('user', text);

  var agentMsg = appendMessage('agent', '');
  var bubble = agentMsg.querySelector('.msg-bubble');
  var typingEl = createTypingIndicator();
  bubble.appendChild(typingEl);

  var fullContent = '';
  var thinkingContent = '';
  var thinkingEl = null;
  currentTaskId = null;
  abortController = new AbortController();

  try {
    // 1. 启动任务
    var startResp = await fetch(API + '/api/agent/run-task', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token
      },
      body: JSON.stringify({ prompt: text, session_id: currentSessionId }),
      signal: abortController.signal
    });

    if (startResp.status === 401) { handleLogout(); return; }
    if (!startResp.ok) {
      var errData = await startResp.json();
      if (typingEl.parentNode) typingEl.remove();
      bubble.textContent = 'Error: ' + (errData.error || 'start failed');
      isRunning = false;
      abortController = null;
      updateStatus('ready');
      setRunningUI(false);
      return;
    }

    var taskData = await startResp.json();
    currentTaskId = taskData.task_id;

    // 2. SSE 订阅任务输出
    var r = await fetch(API + '/api/agent/stream-task/' + currentTaskId, {
      method: 'GET',
      headers: {
        'Authorization': 'Bearer ' + token
      },
      signal: abortController.signal
    });

    if (r.status === 401) { handleLogout(); return; }

    const reader = r.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    streamLoop: while (true) {
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

        if (d.source === 'thinking' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          thinkingContent += d.content;
          if (!thinkingEl) {
            thinkingEl = document.createElement('div');
            thinkingEl.className = 'msg-thinking';
            bubble.appendChild(thinkingEl);
          }
          thinkingEl.innerHTML = '<div class="thinking-header">💭 思考中…</div><div class="thinking-body">' + escHtml(thinkingContent) + '</div>';
          var container = document.getElementById('chatMessages');
          container.scrollTop = container.scrollHeight;
        } else if (d.source === 'final' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          if (thinkingEl) {
            thinkingEl.innerHTML = '<div class="thinking-header" onclick="this.nextElementSibling.style.display=this.nextElementSibling.style.display==\'none\'?\'block\':\'none\'">💭 思考过程 (' + thinkingContent.length + ' 字符) — 点击折叠/展开</div><div class="thinking-body" style="display:none">' + escHtml(thinkingContent) + '</div>';
          }
          fullContent += d.content;
          bubble.innerHTML = renderMarkdown(fullContent);
          if (thinkingEl) bubble.insertBefore(thinkingEl, bubble.firstChild);
          const container = document.getElementById('chatMessages');
          container.scrollTop = container.scrollHeight;
        } else if (d.source === 'tool' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          var toolEl = document.createElement('div');
          toolEl.className = 'msg-tool-step';
          toolEl.innerHTML = renderToolStep(d.content);
          bubble.appendChild(toolEl);
          var container = document.getElementById('chatMessages');
          container.scrollTop = container.scrollHeight;
        } else if (d.source === 'compact' && d.content) {
          // 上下文压缩提示
          if (typingEl.parentNode) typingEl.remove();
          var compactEl = document.createElement('div');
          compactEl.className = 'msg-tool-step';
          compactEl.style.opacity = '0.7';
          compactEl.innerHTML = '<small>' + d.content + '</small>';
          bubble.appendChild(compactEl);
        } else if (d.source === 'error' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          fullContent += d.content;
          bubble.innerHTML = renderMarkdown(fullContent);
        } else if (d.done || d.source === 'task_end') {
          if (typingEl.parentNode) typingEl.remove();
          if (!fullContent && d.content) {
            fullContent = d.content;
            bubble.innerHTML = renderMarkdown(fullContent);
          } else if (!fullContent) {
            bubble.innerHTML = renderMarkdown('Task completed.');
          }
          break streamLoop;
        }
      }
    }
  } catch (err) {
    if (err.name === 'AbortError') {
      // 调用 abort-task 接口
      if (currentTaskId) {
        fetch(API + '/api/agent/abort-task/' + currentTaskId, {
          method: 'POST',
          headers: { 'Authorization': 'Bearer ' + token }
        }).catch(()=>{});
      }
      if (typingEl.parentNode) typingEl.remove();
      if (!fullContent) {
        bubble.innerHTML = renderMarkdown('_Stopped._');
      } else {
        fullContent += '\n\n_Stopped._';
        bubble.innerHTML = renderMarkdown(fullContent);
      }
    } else {
      if (typingEl.parentNode) typingEl.remove();
      bubble.textContent = 'Network error: ' + err.message;
    }
  }

  isRunning = false;
  currentTaskId = null;
  abortController = null;
  updateStatus('ready');
  setRunningUI(false);
  loadFiles();
}

function stopMessage() {
  if (abortController) {
    abortController.abort();
  }
}

function setRunningUI(running) {
  document.getElementById('sendBtn').style.display = running ? 'none' : '';
  document.getElementById('stopBtn').style.display = running ? '' : 'none';
  document.getElementById('chatInput').disabled = running;
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
        if (m.role === 'tool') {
          appendToolHistory(m.content);
        } else {
          appendMessage(m.role, m.content);
        }
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
    .replace(/<summary>[\s\S]*?<\/summary>/g, '')
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
  if (cfg.type === 'file') {
    var fileName = cfg.name || cfg.path || 'File';
    var filePath = cfg.path || '';
    var fileIcon = '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>';
    var descHtml = cfg.description ? '<div class="icard-file-desc">' + escHtml(cfg.description) + '</div>' : '';
    return '<div class="interactive-card" id="' + cardId + '">' +
      '<div class="icard-file-row">' +
        '<div class="icard-file-icon">' + fileIcon + '</div>' +
        '<div class="icard-file-info">' +
          '<div class="icard-file-name">' + escHtml(fileName) + '</div>' +
          descHtml +
        '</div>' +
        '<div class="icard-file-actions">' +
          '<button class="icard-file-btn icard-file-preview" onclick="previewFile(\'' + escAttr(filePath) + '\',\'' + escAttr(fileName) + '\')">' +
            '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>' +
            'Preview' +
          '</button>' +
          '<button class="icard-file-btn icard-file-download" onclick="downloadFile(\'' + escAttr(filePath) + '\')">' +
            '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>' +
            'Download' +
          '</button>' +
        '</div>' +
      '</div>' +
    '</div>';
  }
  if (cfg.type === 'choice') {
    var allOpts = cfg.options || [];
    var previewSlugs = cfg.preview_slugs || [];
    var pageSize = 5;
    var totalPages = Math.ceil(allOpts.length / pageSize);

    function renderChoiceBtn(opt, i) {
      var slug = previewSlugs[i] || '';
      var previewBtn = slug ? '<button class="icard-preview-btn" onclick="event.stopPropagation();previewTemplateSlug(\'' + escAttr(slug) + '\')" title="Preview template">' +
        '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>' +
      '</button>' : '';
      return '<div class="icard-choice-row">' +
        '<button class="icard-choice-btn" onclick="submitInteractiveChoice(\'' + cardId + '\',\'' + escAttr(cfg.id || '') + '\',\'' + escAttr(opt) + '\')">' + escHtml(opt) + '</button>' +
        previewBtn +
      '</div>';
    }

    if (allOpts.length <= pageSize) {
      var opts = allOpts.map(renderChoiceBtn).join('');
      return '<div class="interactive-card" id="' + cardId + '">' +
        '<div class="icard-question">' + escHtml(cfg.question || '') + '</div>' +
        '<div class="icard-choices">' + opts + '</div>' +
      '</div>';
    }

    var pagesHtml = '';
    for (var p = 0; p < totalPages; p++) {
      var pageOpts = allOpts.slice(p * pageSize, (p + 1) * pageSize);
      var pageIdxStart = p * pageSize;
      var pageBtns = pageOpts.map(function(opt, i) {
        return renderChoiceBtn(opt, pageIdxStart + i);
      }).join('');
      pagesHtml += '<div class="icard-page' + (p === 0 ? ' active' : '') + '" data-page="' + p + '">' +
        '<div class="icard-choices">' + pageBtns + '</div>' +
      '</div>';
    }

    var paginationHtml = totalPages > 1 ? '<div class="icard-pagination">' +
      '<button class="icard-page-btn icard-page-prev" onclick="flipChoicePage(event,\'' + cardId + '\',-1)" disabled>' +
        '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="15 18 9 12 15 6"/></svg>' +
      '</button>' +
      '<span class="icard-page-info">1/' + totalPages + '</span>' +
      '<button class="icard-page-btn icard-page-next" onclick="flipChoicePage(event,\'' + cardId + '\',1)">' +
        '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="9 18 15 12 9 6"/></svg>' +
      '</button>' +
    '</div>' : '';

    return '<div class="interactive-card" id="' + cardId + '">' +
      '<div class="icard-question">' + escHtml(cfg.question || '') + '</div>' +
      '<div class="icard-pages">' + pagesHtml + '</div>' +
      paginationHtml +
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

function flipChoicePage(event, cardId, dir) {
  event.preventDefault();
  var card = document.getElementById(cardId);
  if (!card) return;
  var pages = card.querySelectorAll('.icard-page');
  var activeIdx = -1;
  for (var i = 0; i < pages.length; i++) {
    if (pages[i].classList.contains('active')) { activeIdx = i; break; }
  }
  var newIdx = activeIdx + dir;
  if (newIdx < 0 || newIdx >= pages.length) return;
  pages[activeIdx].classList.remove('active');
  pages[newIdx].classList.add('active');
  var info = card.querySelector('.icard-page-info');
  if (info) info.textContent = (newIdx + 1) + '/' + pages.length;
  var prev = card.querySelector('.icard-page-prev');
  var next = card.querySelector('.icard-page-next');
  if (prev) prev.disabled = newIdx === 0;
  if (next) next.disabled = newIdx === pages.length - 1;
}

async function sendMessageText(text) {
  if (isRunning) return;
  isRunning = true;
  updateStatus('running');
  setRunningUI(true);

  appendMessage('user', text);

  var agentMsg = appendMessage('agent', '');
  var bubble = agentMsg.querySelector('.msg-bubble');
  var typingEl = createTypingIndicator();
  bubble.appendChild(typingEl);

  var fullContent = '';
  var thinkingContent = '';
  var thinkingEl = null;
  abortController = new AbortController();

  try {
    var r = await fetch(API + '/api/agent/stream', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token
      },
      body: JSON.stringify({ prompt: text, session_id: currentSessionId }),
      signal: abortController.signal
    });

    if (r.status === 401) { handleLogout(); return; }

    var reader = r.body.getReader();
    var decoder = new TextDecoder();
    var buffer = '';

    streamLoop2: while (true) {
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

        if (d.source === 'thinking' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          thinkingContent += d.content;
          if (!thinkingEl) {
            thinkingEl = document.createElement('div');
            thinkingEl.className = 'msg-thinking';
            bubble.appendChild(thinkingEl);
          }
          thinkingEl.innerHTML = '<div class="thinking-header">💭 思考中…</div><div class="thinking-body">' + escHtml(thinkingContent) + '</div>';
          var container = document.getElementById('chatMessages');
          container.scrollTop = container.scrollHeight;
        } else if (d.source === 'final' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          if (thinkingEl) {
            thinkingEl.innerHTML = '<div class="thinking-header" onclick="this.nextElementSibling.style.display=this.nextElementSibling.style.display==\'none\'?\'block\':\'none\'">💭 思考过程 (' + thinkingContent.length + ' 字符) — 点击折叠/展开</div><div class="thinking-body" style="display:none">' + escHtml(thinkingContent) + '</div>';
          }
          fullContent += d.content;
          bubble.innerHTML = renderMarkdown(fullContent);
          if (thinkingEl) bubble.insertBefore(thinkingEl, bubble.firstChild);
          var container = document.getElementById('chatMessages');
          container.scrollTop = container.scrollHeight;
        } else if (d.source === 'tool' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          var toolEl = document.createElement('div');
          toolEl.className = 'msg-tool-step';
          toolEl.innerHTML = renderToolStep(d.content);
          bubble.appendChild(toolEl);
          var container = document.getElementById('chatMessages');
          container.scrollTop = container.scrollHeight;
        } else if (d.source === 'error' && d.content) {
          if (typingEl.parentNode) typingEl.remove();
          fullContent += d.content;
          bubble.innerHTML = renderMarkdown(fullContent);
        } else if (d.done || d.source === 'task_end') {
          if (typingEl.parentNode) typingEl.remove();
          if (!fullContent && d.content) {
            fullContent = d.content;
            bubble.innerHTML = renderMarkdown(fullContent);
          } else if (!fullContent) {
            bubble.innerHTML = renderMarkdown('Task completed.');
          }
          break streamLoop2;
        }
      }
    }
  } catch (err) {
    if (err.name === 'AbortError') {
      if (typingEl.parentNode) typingEl.remove();
      if (!fullContent) {
        bubble.innerHTML = renderMarkdown('_Stopped._');
      } else {
        fullContent += '\n\n_Stopped._';
        bubble.innerHTML = renderMarkdown(fullContent);
      }
    } else {
      if (typingEl.parentNode) typingEl.remove();
      bubble.textContent = 'Network error: ' + err.message;
    }
  }

  isRunning = false;
  abortController = null;
  updateStatus('ready');
  setRunningUI(false);
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

// Editor styles injected into preview iframe (adapted from web-editor.html)
var _editorInjectedStyle = [
  '[data-editable="true"] { outline: 1px dashed transparent; transition: outline-color .15s, box-shadow .15s; cursor: text; }',
  '[data-editable="true"]:hover { outline-color: rgba(37,64,255,.5); }',
  '[data-editable="true"]:focus { outline: 2px solid #2540FF; outline-offset: 2px; box-shadow: 0 0 0 4px rgba(37,64,255,.14); }',
  '[data-media-editable="true"] { cursor: pointer; transition: filter .15s, outline-color .15s; outline: 2px solid transparent; }',
  '[data-media-editable="true"]:hover { filter: brightness(.92); outline-color: rgba(37,64,255,.72); }',
  '[data-media-selected="true"] { outline: 2px solid #2540FF !important; box-shadow: 0 0 0 4px rgba(37,64,255,.16); }',
  '[data-media-placeholder="true"] { position: relative; cursor: pointer; min-height: 80px; min-width: 80px; outline: 2px dashed rgba(37,64,255,.62); outline-offset: -2px; background-color: rgba(10,10,10,.74); transition: background-color .15s, outline-color .15s; display: flex; align-items: center; justify-content: center; }',
  '[data-media-placeholder="true"]:hover { background-color: rgba(37,64,255,.16); outline-color: #2540FF; }',
  '[data-media-placeholder="true"]::after { content: "\\FF0B  Insert Image"; pointer-events: none; font: 700 11px/1 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; letter-spacing: .08em; color: #2540FF; padding: 7px 12px 6px; border-radius: 999px; background: #F2EBDD; box-shadow: 0 12px 26px rgba(0,0,0,.22); }',
  '[data-media-placeholder="true"][data-media-kind="video"]::after { content: "\\FF0B  Insert Video"; }',
  '[data-media-placeholder="true"] > *:not(source):not(track) { opacity: .35; pointer-events: none; }',
  '#__editor_toolbar__ { position: absolute; z-index: 2147483647; display: inline-flex; align-items: center; gap: 2px; padding: 4px; border-radius: 999px; border: 1px solid #2A2A2A; background: rgba(10,10,10,.94); backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px); box-shadow: 0 14px 34px rgba(0,0,0,.36), 0 0 0 1px rgba(242,235,221,.04) inset; font: 700 12px/1 "PingFang SC","Microsoft YaHei",sans-serif; color: #F2EBDD; opacity: 0; transform: translateY(4px); pointer-events: none; transition: opacity .14s ease, transform .14s ease; user-select: none; }',
  '#__editor_toolbar__.show { opacity: 1; transform: translateY(0); pointer-events: auto; }',
  '#__editor_toolbar__ button { appearance: none; border: 0; background: transparent; color: #F2EBDD; min-width: 30px; height: 28px; padding: 0 10px; border-radius: 999px; font: inherit; font-weight: 600; cursor: pointer; transition: background .12s ease, color .12s ease; }',
  '#__editor_toolbar__ button:hover { background: #2540FF; color: #F2EBDD; }',
  '#__editor_toolbar__ button:active { background: #4A60FF; }',
  '#__editor_toolbar__ .sep { width: 1px; height: 18px; background: #2A2A2A; margin: 0 2px; }',
  '#__editor_toolbar__ .label { padding: 0 8px; color: #FF5A2C; font: 700 11px/1 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; letter-spacing: .08em; text-transform: uppercase; }',
  '#__editor_toolbar__ .scale { appearance: none; border: 0; outline: 0; width: 62px; height: 24px; padding: 0 8px; border-radius: 999px; background: #141414; color: #F2EBDD; font: inherit; font-size: 12px; font-variant-numeric: tabular-nums; font-weight: 700; text-align: center; user-select: text; transition: background .12s ease; }',
  '#__editor_toolbar__ .scale:hover { background: #1D1D1D; }',
  '#__editor_toolbar__ .scale:focus { background: #1D1D1D; box-shadow: 0 0 0 2px rgba(255,90,44,.32); }'
].join('\n');

// Editor runtime script injected into preview iframe (adapted from web-editor.html)
// Uses parent window's __editorUploadAsset for image/video replacement via upload API
var _editorRuntimeScript = [
  '(function(){',
  'var EDITABLE_TAGS=["P","SPAN","H1","H2","H3","H4","H5","H6","LI","A","BUTTON","LABEL","TD","TH","BLOCKQUOTE","FIGCAPTION","STRONG","EM","SMALL","DT","DD","SUMMARY","CAPTION","DIV"];',
  'var set=new Set(EDITABLE_TAGS);',
  'var FS_MIN=8,FS_MAX=240,IMG_MIN=24,IMG_MAX=4000,STEP=1.1;',
  'function hasOnlyTextOrInline(el){for(var n of el.childNodes){if(n.nodeType===3&&n.nodeValue.trim())return true;}return false;}',
  'var IMG_PH_SEL="[data-img-slot],[data-placeholder],.img-slot,.image-slot,.img-placeholder,.placeholder-img";',
  'var VID_PH_SEL="[data-video-slot],.video-slot,.video-placeholder,.placeholder-video";',
  'var IMG_CLS_RE=/\\b(img-slot|image-slot|img-placeholder|placeholder-img)\\b/g;',
  'var VID_CLS_RE=/\\b(video-slot|video-placeholder|placeholder-video)\\b/g;',
  'function isEmptyImg(img){var s=(img.getAttribute("src")||"").trim();if(!s)return true;if(/^(about:blank|#|data:,)$/i.test(s))return true;if(/placeholder/i.test(s))return true;return img.complete&&img.naturalWidth===0;}',
  'function isEmptyVideo(v){var s=(v.getAttribute("src")||"").trim();if(s&&!/placeholder/i.test(s))return false;for(var src of v.querySelectorAll("source")){var ss=(src.getAttribute("src")||"").trim();if(ss&&!/placeholder/i.test(ss))return false;}return true;}',
  'function markContainerPH(el,kind){if(el.tagName==="IMG"||el.tagName==="VIDEO")return;if(el.hasAttribute("data-media-placeholder"))return;el.setAttribute("data-media-placeholder","true");el.setAttribute("data-media-kind",kind);el.addEventListener("click",onContainerPHClick);}',
  'function setupAsMedia(el){el.setAttribute("data-media-editable","true");el.setAttribute("data-media-kind",el.tagName==="VIDEO"?"video":"image");el.addEventListener("click",onMediaClick);}',
  'function makeEditable(root){root.querySelectorAll("*").forEach(function(el){if(set.has(el.tagName)&&hasOnlyTextOrInline(el)){el.setAttribute("contenteditable","true");el.setAttribute("data-editable","true");}});',
  'root.querySelectorAll(IMG_PH_SEL).forEach(function(el){markContainerPH(el,"image");});',
  'root.querySelectorAll(VID_PH_SEL).forEach(function(el){markContainerPH(el,"video");});',
  'root.querySelectorAll("img").forEach(function(img){if(isEmptyImg(img)){markElementPH(img,"image");img.addEventListener("error",function(){markElementPH(img,"image");});}else setupAsMedia(img);});',
  'root.querySelectorAll("video").forEach(function(v){if(isEmptyVideo(v))markElementPH(v,"video");else setupAsMedia(v);});}',
  'function markElementPH(el,kind){el.setAttribute("data-media-placeholder","true");el.setAttribute("data-media-kind",kind);el.style.minWidth=el.style.minWidth||"120px";el.style.minHeight=el.style.minHeight||"80px";el.addEventListener("click",onElementPHClick);}',
  'function onElementPHClick(e){e.preventDefault();e.stopPropagation();var el=e.currentTarget;var kind=el.getAttribute("data-media-kind")||(el.tagName==="VIDEO"?"video":"image");pickFile(kind,function(file){applyMediaFile(file,kind,function(src){if(el.tagName==="VIDEO"){el.querySelectorAll("source").forEach(function(n){n.remove();});if(!el.hasAttribute("controls"))el.setAttribute("controls","");}el.src=src;el.removeAttribute("data-media-placeholder");el.style.minWidth="";el.style.minHeight="";setupAsMedia(el);});});}',
  'function pickFile(kind,cb){var input=document.createElement("input");input.type="file";input.accept=kind==="video"?"video/*":"image/*";input.addEventListener("change",function(){var f=input.files&&input.files[0];if(f)cb(f);});input.click();}',
  'async function applyMediaFile(file,kind,applyFn){try{var res=await window.parent.__editorUploadAsset(file,kind);if(res&&res.url)applyFn(res.url);else applyFn("");}catch(e){var r=new FileReader();r.onload=function(){applyFn(r.result);};r.readAsDataURL(file);}}',
  'function onContainerPHClick(e){e.preventDefault();e.stopPropagation();var el=e.currentTarget;var kind=el.getAttribute("data-media-kind")||"image";pickFile(kind,function(file){applyMediaFile(file,kind,function(src){var tag=kind==="video"?"video":"img";var m=document.createElement(tag);m.src=src;if(kind==="video"){m.controls=true;m.muted=true;m.playsInline=true;}var classRe=kind==="video"?VID_CLS_RE:IMG_CLS_RE;if(el.className)m.className=el.className.replace(classRe,"").trim();if(el.id)m.id=el.id;var inline=el.getAttribute("style");if(inline)m.setAttribute("style",inline);var cs=window.getComputedStyle(el);if(!m.style.width&&cs.width!=="auto")m.style.width=cs.width;if(!m.style.height&&cs.height!=="auto"&&el.style.aspectRatio===""&&!inline?.includes("aspect-ratio"))m.style.height=cs.height;m.style.objectFit=kind==="video"?"contain":(m.style.objectFit||"cover");setupAsMedia(m);el.replaceWith(m);});});}',
  // Floating toolbar
  'var toolbar=document.createElement("div");toolbar.id="__editor_toolbar__";document.body.appendChild(toolbar);var activeTarget=null;',
  'function btn(label,title,onClick){var b=document.createElement("button");b.type="button";b.textContent=label;b.title=title;b.addEventListener("mousedown",function(e){e.preventDefault();});b.addEventListener("click",function(e){e.preventDefault();e.stopPropagation();onClick();});return b;}',
  'function sep(){var s=document.createElement("span");s.className="sep";return s;}',
  'function lbl(t){var s=document.createElement("span");s.className="label";s.textContent=t;return s;}',
  'function fmtScale(n){if(!Number.isFinite(n)||n<=0)n=1;return n.toFixed(2).replace(/\\.00$/,"")+"\\u00d7";}',
  'function parseScaleInput(raw){var text=String(raw||"").trim();if(!text)return null;text=text.replace(/，/g,".").replace(/×/g,"x").toLowerCase();var scale;if(text.endsWith("%")){scale=parseFloat(text.slice(0,-1))/100;}else{scale=parseFloat(text.replace(/x$/,""));}return Number.isFinite(scale)&&scale>0?scale:null;}',
  'function scaleInputEl(target,value,applyValue,readValue){var input=document.createElement("input");input.className="scale";input.type="text";input.inputMode="decimal";input.value=fmtScale(value);input.title="Enter scale, e.g. 1.25 or 125%";var skipBlur=false;var revert=function(){input.value=fmtScale(readValue());};var commit=function(){var parsed=parseScaleInput(input.value);if(parsed==null){revert();return;}var applied=applyValue(parsed);input.value=fmtScale(applied);activeTarget=target;positionToolbar(target);};input.addEventListener("mousedown",function(e){e.stopPropagation();});input.addEventListener("click",function(e){e.stopPropagation();});input.addEventListener("focus",function(){activeTarget=target;input.select();});input.addEventListener("keydown",function(e){e.stopPropagation();if(e.key==="Enter"){e.preventDefault();commit();input.select();}else if(e.key==="Escape"){e.preventDefault();skipBlur=true;revert();input.blur();}});input.addEventListener("blur",function(){if(skipBlur){skipBlur=false;return;}commit();});return input;}',
  'function updateScaleBadge(value){var s=toolbar.querySelector(".scale");if(s)s.value=fmtScale(value);}',
  'function ensureFontBase(el){var base=parseFloat(el.dataset.editorBaseFontSize);if(!Number.isFinite(base)||base<=0){base=parseFloat(window.getComputedStyle(el).fontSize)||16;el.dataset.editorBaseFontSize=base.toFixed(4);}return base;}',
  'function getFontScale(el){var base=ensureFontBase(el);var cur=parseFloat(window.getComputedStyle(el).fontSize)||base;return cur/base;}',
  'function currentMediaWidth(el){return el.getBoundingClientRect().width||el.naturalWidth||el.videoWidth||200;}',
  'function ensureMediaBase(el){var base=parseFloat(el.dataset.editorBaseMediaWidth);if(!Number.isFinite(base)||base<=0){base=currentMediaWidth(el);el.dataset.editorBaseMediaWidth=base.toFixed(4);}return base;}',
  'function getMediaScale(el){var base=ensureMediaBase(el);return currentMediaWidth(el)/base;}',
  'function mediaKind(el){return el.getAttribute("data-media-kind")||(el.tagName==="VIDEO"?"video":"image");}',
  'function applyMediaScaleStyles(el,width){el.style.width=width.toFixed(1)+"px";el.style.height="auto";el.style.maxWidth="none";if(mediaKind(el)==="video")el.style.objectFit="contain";else el.style.objectFit=el.style.objectFit||"";}',
  'function positionToolbar(target){var r=target.getBoundingClientRect();var sx=window.scrollX,sy=window.scrollY;toolbar.style.visibility="hidden";toolbar.classList.add("show");var tw=toolbar.offsetWidth,th=toolbar.offsetHeight;var top=r.top+sy-th-10;if(top<sy+4)top=r.bottom+sy+8;var left=r.left+sx+(r.width-tw)/2;left=Math.max(sx+6,Math.min(left,sx+document.documentElement.clientWidth-tw-6));toolbar.style.top=top+"px";toolbar.style.left=left+"px";toolbar.style.visibility="";}',
  'function showTextToolbar(el){activeTarget=el;toolbar.innerHTML="";ensureFontBase(el);toolbar.appendChild(lbl("Font"));toolbar.appendChild(scaleInputEl(el,getFontScale(el),function(v){return setFontScale(el,v);},function(){return getFontScale(el);}));toolbar.appendChild(btn("A-","Smaller",function(){scaleFont(el,1/STEP);}));toolbar.appendChild(btn("A+","Bigger",function(){scaleFont(el,STEP);}));toolbar.appendChild(sep());toolbar.appendChild(btn("\\u00d7","Close",hideToolbar));positionToolbar(el);}',
  'function showMediaToolbar(el){activeTarget=el;var kind=mediaKind(el);var isVideo=kind==="video";toolbar.innerHTML="";ensureMediaBase(el);toolbar.appendChild(lbl(isVideo?"Video":"Image"));toolbar.appendChild(scaleInputEl(el,getMediaScale(el),function(v){return setMediaScale(el,v);},function(){return getMediaScale(el);}));toolbar.appendChild(btn("-","Smaller",function(){scaleMedia(el,1/STEP);}));toolbar.appendChild(btn("+","Bigger",function(){scaleMedia(el,STEP);}));toolbar.appendChild(sep());toolbar.appendChild(btn("Replace",isVideo?"Replace video":"Replace image",function(){replaceMedia(el);}));toolbar.appendChild(btn("\\u00d7","Deselect",function(){el.removeAttribute("data-media-selected");hideToolbar();}));positionToolbar(el);}',
  'function hideToolbar(){toolbar.classList.remove("show");if(activeTarget&&activeTarget.getAttribute&&activeTarget.getAttribute("data-media-editable"))activeTarget.removeAttribute("data-media-selected");activeTarget=null;}',
  'function scaleFont(el,factor){var base=ensureFontBase(el);var cur=parseFloat(window.getComputedStyle(el).fontSize)||16;var next=Math.max(FS_MIN,Math.min(FS_MAX,cur*factor));el.style.fontSize=next.toFixed(2)+"px";updateScaleBadge(next/base);positionToolbar(el);}',
  'function setFontScale(el,scale){var base=ensureFontBase(el);var next=Math.max(FS_MIN,Math.min(FS_MAX,base*scale));el.style.fontSize=next.toFixed(2)+"px";updateScaleBadge(next/base);return next/base;}',
  'function scaleMedia(el,factor){var base=ensureMediaBase(el);var curW=el.getBoundingClientRect().width||el.naturalWidth||el.videoWidth||200;var next=Math.max(IMG_MIN,Math.min(IMG_MAX,curW*factor));applyMediaScaleStyles(el,next);updateScaleBadge(next/base);requestAnimationFrame(function(){positionToolbar(el);});}',
  'function setMediaScale(el,scale){var base=ensureMediaBase(el);var next=Math.max(IMG_MIN,Math.min(IMG_MAX,base*scale));applyMediaScaleStyles(el,next);updateScaleBadge(next/base);requestAnimationFrame(function(){positionToolbar(el);});return next/base;}',
  'function replaceMedia(el){var kind=mediaKind(el);pickFile(kind,function(file){applyMediaFile(file,kind,function(src){if(el.tagName==="VIDEO")el.querySelectorAll("source").forEach(function(n){n.remove();});el.src=src;requestAnimationFrame(function(){positionToolbar(el);});});});}',
  'function onMediaClick(e){e.preventDefault();e.stopPropagation();var el=e.currentTarget;document.querySelectorAll("[data-media-selected]").forEach(function(n){n.removeAttribute("data-media-selected");});el.setAttribute("data-media-selected","true");showMediaToolbar(el);}',
  'document.addEventListener("focusin",function(e){var el=e.target;if(el&&el.getAttribute&&el.getAttribute("data-editable")==="true")showTextToolbar(el);});',
  'document.addEventListener("mousedown",function(e){if(toolbar.contains(e.target))return;var t=e.target;var isEditable=t.closest&&t.closest("[data-editable=\\"true\\"]");var isMedia=t.closest&&t.closest("[data-media-editable=\\"true\\"]");if(!isEditable&&!isMedia)hideToolbar();},true);',
  'window.addEventListener("scroll",function(){if(activeTarget)positionToolbar(activeTarget);},true);',
  'window.addEventListener("resize",function(){if(activeTarget)positionToolbar(activeTarget);});',
  'document.addEventListener("click",function(e){var a=e.target.closest&&e.target.closest("a");if(a)e.preventDefault();},true);',
  'makeEditable(document.body);',
  '})();'
].join('\n');

// Bridge function called by iframe to upload image/video assets
// Returns {url: absolute_url} on success
function __editorUploadAsset(file, kind) {
  var formData = new FormData();
  formData.append('file', file);
  // Upload to same directory as the HTML file
  var dir = __editorUploadAsset._currentPath ? __editorUploadAsset._currentPath.replace(/[^/]*$/, '') : '';
  formData.append('path', dir + file.name);

  return fetch(API + '/api/workspace/upload?token=' + token, {
    method: 'POST',
    body: formData
  }).then(function(r) {
    if (!r.ok) throw new Error('Upload failed');
    return r.json();
  }).then(function(data) {
    // Return the preview URL for the uploaded file
    var uploadedPath = data.path || (dir + file.name);
    return { url: API + '/api/workspace/preview?path=' + encodeURIComponent(uploadedPath) + '&token=' + token };
  });
}

function previewFile(path, name) {
  // Determine extension from path (has the actual filename), fallback to name
  var ext = (path || '').split('.').pop().toLowerCase();
  if ((path || '').indexOf('.') === -1) {
    ext = (name || '').split('.').pop().toLowerCase();
  }
  var imageExts = ['png','jpg','jpeg','gif','webp','bmp','ico','svg'];
  var textExts = ['txt','md','json','yaml','yml','toml','ini','cfg','conf','env','csv','log','py','go','js','ts','tsx','jsx','vue','svelte','css','scss','less','sh','bash','zsh','java','c','cpp','h','hpp','rs','rb','php','sql','xml'];
  var videoExts = ['mp4'];
  var audioExts = ['mp3','wav'];
  var pdfExts = ['pdf'];
  var htmlExts = ['html','htm'];

  var tabId = 'file:' + path;
  var previewUrl = API + '/api/workspace/preview?path=' + encodeURIComponent(path) + '&token=' + token;

  if (imageExts.indexOf(ext) !== -1) {
    openPreviewTab(tabId, name || path, false, function(panel) {
      panel.innerHTML = '<img src="' + previewUrl + '" style="max-width:100%;max-height:80vh;border-radius:8px;display:block" alt="' + escHtml(name) + '">';
    });
  } else if (videoExts.indexOf(ext) !== -1) {
    openPreviewTab(tabId, name || path, false, function(panel) {
      panel.innerHTML = '<video src="' + previewUrl + '" controls style="max-width:100%;max-height:80vh;border-radius:8px;display:block"></video>';
    });
  } else if (audioExts.indexOf(ext) !== -1) {
    openPreviewTab(tabId, name || path, false, function(panel) {
      panel.innerHTML = '<div style="padding:2rem;text-align:center"><div style="font-size:3rem;margin-bottom:1rem">&#9835;</div><audio src="' + previewUrl + '" controls style="width:100%"></audio></div>';
    });
  } else if (pdfExts.indexOf(ext) !== -1) {
    openPreviewTab(tabId, name || path, false, function(panel) {
      panel.innerHTML = '<iframe src="' + previewUrl + '" style="width:100%;height:80vh;border:none;border-radius:8px"></iframe>';
    });
  } else if (htmlExts.indexOf(ext) !== -1) {
    // Check if this is a skill template (no edit for templates)
    var isTemplate = (path || '').indexOf('skills/') === 0;
    // Set current path for asset uploads
    if (!isTemplate) __editorUploadAsset._currentPath = path;
    openPreviewTab(tabId, name || path, true, function(panel) {
      panel.innerHTML = '<div class="preview-loading">Loading preview...</div>';
      fetch(previewUrl).then(function(r) {
        if (!r.ok) { panel.innerHTML = '<div style="padding:2rem;color:var(--danger)">Failed to load file</div>'; return; }
        return r.text();
      }).then(function(html) {
        if (!html) return;
        // Toolbar for editing
        var toolbar = '';
        if (!isTemplate) {
          toolbar = '<div class="preview-edit-bar">' +
            '<span class="preview-edit-hint">Click text to edit · Click image/video to replace</span>' +
            '<button class="preview-edit-btn" onclick="saveHtmlPreview(\'' + escAttr(path) + '\',this)">Save</button>' +
          '</div>';
        }
        var wrap = document.createElement('div');
        wrap.className = 'preview-html-wrap';
        wrap.innerHTML = toolbar;
        var iframe = document.createElement('iframe');
        iframe.sandbox = 'allow-scripts allow-same-origin';
        iframe.className = 'preview-iframe';
        wrap.appendChild(iframe);
        panel.innerHTML = '';
        panel.appendChild(wrap);
        iframe.srcdoc = html;
        // Inject editing behavior after iframe loads
        if (!isTemplate) {
          iframe.onload = function() {
            try {
              var doc = iframe.contentDocument || iframe.contentWindow.document;
              // Inject editor styles
              var style = doc.createElement('style');
              style.setAttribute('data-injected-edit', 'true');
              style.textContent = _editorInjectedStyle;
              doc.head.appendChild(style);
              // Inject editor runtime
              var script = doc.createElement('script');
              script.setAttribute('data-injected-edit', 'true');
              script.textContent = _editorRuntimeScript;
              doc.body.appendChild(script);
            } catch(e) { console.error('Editor inject failed', e); }
          };
        }
      }).catch(function(e) {
        panel.innerHTML = '<div style="padding:2rem;color:var(--danger)">Error loading preview</div>';
      });
    });
  } else if (textExts.indexOf(ext) !== -1 || ext === '') {
    openPreviewTab(tabId, name || path, false, function(panel) {
      panel.innerHTML = '<div class="preview-loading">Loading preview...</div>';
      fetch(previewUrl).then(function(r) {
        if (!r.ok) { panel.innerHTML = '<div style="padding:2rem;color:var(--danger)">Failed to load file</div>'; return; }
        return r.text();
      }).then(function(text) {
        var lines = text.split('\n');
        var maxLines = 500;
        var truncated = lines.length > maxLines;
        if (truncated) text = lines.slice(0, maxLines).join('\n');
        var escaped = text.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
        panel.innerHTML = '<pre class="preview-code">' + escaped + (truncated ? '\n\n... (showing first ' + maxLines + ' of ' + lines.length + ' lines)' : '') + '</pre>';
      }).catch(function(e) {
        panel.innerHTML = '<div style="padding:2rem;color:var(--danger)">Error: ' + escHtml(e.message) + '</div>';
      });
    });
  } else {
    openPreviewTab(tabId, name || path, false, function(panel) {
      panel.innerHTML = '<div style="padding:2rem;text-align:center;color:var(--text-3)"><p>Preview not available for this file type</p><p style="font-size:.8125rem">.' + escHtml(ext) + ' files can be downloaded instead</p></div>';
    });
  }
}

function saveHtmlPreview(path, btn) {
  // Find the iframe in the same preview panel
  var panel = btn.closest('.preview-panel');
  if (!panel) return;
  var iframe = panel.querySelector('.preview-iframe');
  if (!iframe) return;

  try {
    var doc = iframe.contentDocument || iframe.contentWindow.document;
    var clone = doc.documentElement.cloneNode(true);
    // Remove all injected editor elements
    clone.querySelectorAll('[data-injected-edit]').forEach(function(n) { n.remove(); });
    // Remove all editor attributes
    clone.querySelectorAll('[contenteditable]').forEach(function(n) { n.removeAttribute('contenteditable'); });
    clone.querySelectorAll('[data-editable]').forEach(function(n) { n.removeAttribute('data-editable'); });
    clone.querySelectorAll('[data-media-editable]').forEach(function(n) { n.removeAttribute('data-media-editable'); });
    clone.querySelectorAll('[data-media-selected]').forEach(function(n) { n.removeAttribute('data-media-selected'); });
    clone.querySelectorAll('[data-media-placeholder]').forEach(function(n) { n.removeAttribute('data-media-placeholder'); });
    clone.querySelectorAll('[data-media-kind]').forEach(function(n) { n.removeAttribute('data-media-kind'); });
    clone.querySelectorAll('[data-editor-base-font-size]').forEach(function(n) { n.removeAttribute('data-editor-base-font-size'); });
    clone.querySelectorAll('[data-editor-base-media-width]').forEach(function(n) { n.removeAttribute('data-editor-base-media-width'); });
    // Restore original src from data-src-original
    clone.querySelectorAll('[data-src-original]').forEach(function(n) {
      n.setAttribute('src', n.getAttribute('data-src-original'));
      n.removeAttribute('data-src-original');
    });
    // Clean up PPT runtime state (nav dots, slide positions)
    _cleanupPresentationRuntimeState(clone);

    var html = '<!doctype html>\n' + clone.outerHTML;

    btn.disabled = true;
    btn.textContent = 'Saving...';

    fetch(API + '/api/workspace/save?token=' + token, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: path, content: html })
    }).then(function(r) {
      if (!r.ok) throw new Error('Save failed');
      return r.json();
    }).then(function(data) {
      btn.textContent = 'Saved!';
      btn.classList.add('saved');
      setTimeout(function() {
        btn.textContent = 'Save';
        btn.classList.remove('saved');
        btn.disabled = false;
      }, 2000);
    }).catch(function(e) {
      btn.textContent = 'Error';
      btn.classList.add('error');
      setTimeout(function() {
        btn.textContent = 'Save';
        btn.classList.remove('error');
        btn.disabled = false;
      }, 2000);
    });
  } catch(e) {
    btn.textContent = 'Error';
    setTimeout(function() { btn.textContent = 'Save'; btn.disabled = false; }, 2000);
  }
}

function _cleanupPresentationRuntimeState(root) {
  var scriptsText = '';
  root.querySelectorAll('script').forEach(function(s) { scriptsText += s.textContent || ''; });
  var isRuntimeSlideDeck =
    root.querySelector('#deck') &&
    root.querySelector('#nav-dots') &&
    root.querySelector('.slide') &&
    /navDots\.appendChild|navDots\.append\(|getElementById\(['"]nav-dots['"]\)/.test(scriptsText);
  if (!isRuntimeSlideDeck) return;
  root.querySelectorAll('#nav-dots').forEach(function(nav) {
    nav.querySelectorAll('.nav-dot').forEach(function(dot) { dot.remove(); });
  });
  var slides = root.querySelectorAll('.slide');
  slides.forEach(function(slide, index) { slide.classList.toggle('is-active', index === 0); });
  var deck = root.querySelector('#deck');
  if (deck) deck.style.removeProperty('transform');
  var progress = root.querySelector('#progress-bar');
  if (progress && slides.length) progress.style.width = (100 / slides.length) + '%';
  var counter = root.querySelector('#slide-counter');
  if (counter && slides.length) counter.textContent = '01 / ' + String(slides.length).padStart(2, '0');
}

function previewTemplateSlug(slug) {
  var tabId = 'tmpl:' + slug;

  openPreviewTab(tabId, slug, true, function(panel) {
    var templateUrl = API + '/api/workspace/preview?path=' + encodeURIComponent('skills/frontend-slides/templates/' + slug + '/template.html') + '&token=' + token;
    panel.innerHTML = '<div class="preview-loading">Loading preview...</div>';

    fetch(templateUrl).then(function(r) {
      if (!r.ok) { panel.innerHTML = '<div style="padding:2rem;color:var(--danger)">Failed to load template</div>'; return Promise.reject(); }
      return r.text();
    }).then(function(html) {
      var basePath = 'skills/frontend-slides/templates/' + slug + '/';
      var fetches = [];

      if (html.indexOf('src="deck-stage.js"') !== -1 || html.indexOf("src='deck-stage.js'") !== -1) {
        var jsUrl = API + '/api/workspace/preview?path=' + encodeURIComponent(basePath + 'deck-stage.js') + '&token=' + token;
        fetches.push(
          fetch(jsUrl).then(function(r) { return r.ok ? r.text() : ''; }).then(function(js) {
            html = html.replace(/<script\s+src=["']deck-stage\.js["']\s*>\s*<\/script>/g, '<script>' + js + '</script>');
          })
        );
      }

      if (html.indexOf('href="styles.css"') !== -1 || html.indexOf("href='styles.css'") !== -1) {
        var cssUrl = API + '/api/workspace/preview?path=' + encodeURIComponent(basePath + 'styles.css') + '&token=' + token;
        fetches.push(
          fetch(cssUrl).then(function(r) { return r.ok ? r.text() : ''; }).then(function(css) {
            html = html.replace(/<link\s+rel=["']stylesheet["']\s+href=["']styles\.css["']\s*\/?>/g, '<style>' + css + '</style>');
          })
        );
      }

      return Promise.all(fetches).then(function() { return html; });
    }).then(function(html) {
      if (!html) return;
      var iframe = document.createElement('iframe');
      iframe.sandbox = 'allow-scripts';
      iframe.className = 'preview-iframe';
      panel.innerHTML = '';
      panel.appendChild(iframe);
      iframe.srcdoc = html;
    }).catch(function(e) {
      if (panel.querySelector('.preview-iframe')) return;
      panel.innerHTML = '<div style="padding:2rem;color:var(--danger)">Error loading preview</div>';
    });
  });
}

var _previewTabs = {};
var _previewTabOrder = [];
var _previewActiveTabId = null;

function openPreviewTab(tabId, title, isWide, renderFn) {
  var modal = document.getElementById('previewModal');
  var tabsBar = document.getElementById('previewTabs');
  var body = document.getElementById('previewBody');

  if (_previewTabs[tabId]) {
    switchPreviewTab(tabId);
    return;
  }

  var panel = document.createElement('div');
  panel.className = 'preview-panel';
  panel.id = 'previewPanel_' + tabId.replace(/[^a-zA-Z0-9_-]/g, '_');

  _previewTabs[tabId] = { title: title, isWide: isWide, panel: panel, loaded: false };
  _previewTabOrder.push(tabId);

  var tabEl = document.createElement('button');
  tabEl.className = 'preview-tab';
  tabEl.title = title;
  tabEl.onclick = function() { switchPreviewTab(tabId); };
  tabEl.innerHTML = '<span class="preview-tab-label">' + escHtml(title) + '</span>' +
    '<span class="preview-tab-close" onclick="event.stopPropagation();closePreviewTab(\'' + tabId + '\')" title="Close tab">' +
      '<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>' +
    '</span>';
  _previewTabs[tabId].tabEl = tabEl;
  tabsBar.appendChild(tabEl);

  body.appendChild(panel);

  switchPreviewTab(tabId);

  if (renderFn) {
    renderFn(panel);
    _previewTabs[tabId].loaded = true;
  }
}

function switchPreviewTab(tabId) {
  var info = _previewTabs[tabId];
  if (!info) return;

  var modal = document.getElementById('previewModal');
  var content = modal.querySelector('.preview-content');

  if (_previewActiveTabId && _previewTabs[_previewActiveTabId]) {
    _previewTabs[_previewActiveTabId].panel.classList.remove('active');
    _previewTabs[_previewActiveTabId].tabEl.classList.remove('active');
  }

  info.panel.classList.add('active');
  info.tabEl.classList.add('active');

  if (info.isWide) {
    content.classList.add('preview-wide');
  } else {
    content.classList.remove('preview-wide');
  }

  _previewActiveTabId = tabId;
  modal.classList.add('show');
}

function closePreviewTab(tabId) {
  var info = _previewTabs[tabId];
  if (!info) return;

  var wasActive = _previewActiveTabId === tabId;

  info.tabEl.remove();
  info.panel.remove();
  delete _previewTabs[tabId];
  _previewTabOrder = _previewTabOrder.filter(function(id) { return id !== tabId; });

  if (wasActive) {
    if (_previewTabOrder.length > 0) {
      switchPreviewTab(_previewTabOrder[_previewTabOrder.length - 1]);
    } else {
      closePreview();
    }
  }
}

function closePreview() {
  var modal = document.getElementById('previewModal');
  var content = modal.querySelector('.preview-content');
  content.classList.remove('preview-wide');
  modal.classList.remove('show');
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

function appendToolHistory(content) {
  const container = document.getElementById('chatMessages');
  const empty = container.querySelector('.empty-state');
  if (empty) empty.remove();

  const msg = document.createElement('div');
  msg.className = 'msg msg-tool-history';
  const card = document.createElement('div');
  card.className = 'msg-tool-step';
  card.innerHTML = renderToolStep(content);
  msg.appendChild(card);
  container.appendChild(msg);
  return msg;
}

function renderToolStep(content) {
  var toolMatch = content.match(/^🛠️\s*(\S+)/);
  var toolName = toolMatch ? toolMatch[1] : 'tool';
  var icon = '';
  if (toolName === 'code_run') icon = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>';
  else if (toolName === 'skill_run') icon = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>';
  else if (toolName === 'file_read') icon = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>';
  else if (toolName === 'file_write') icon = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>';
  else icon = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-2 2 2 2 0 01-2-2v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83 0 2 2 0 010-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 01-2-2 2 2 0 012-2h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 010-2.83 2 2 0 012.83 0l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 012-2 2 2 0 012 2v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 0 2 2 0 010 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 012 2 2 2 0 01-2 2h-.09a1.65 1.65 0 00-1.51 1z"/></svg>';

  var codeMatch = content.match(/````text\n([\s\S]*?)````/);
  var argsHtml = '';
  if (codeMatch) {
    var argsText = codeMatch[1].trim();
    if (argsText.length > 200) argsText = argsText.substring(0, 200) + '...';
    argsHtml = '<div class="tool-step-args"><pre>' + escHtml(argsText) + '</pre></div>';
  }

  return '<div class="tool-step-header">' + icon + '<span class="tool-step-name">' + escHtml(toolName) + '</span></div>' + argsHtml;
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

      var toggle = document.getElementById('sessionToggle');
      if (sessionListVisible) {
        toggle.classList.add('open');
      } else {
        toggle.classList.remove('open');
      }

      loadChatHistory();
    }
  } catch(e) {}
}

function switchSession(id) {
  currentSessionId = parseInt(id, 10);
  var agentNav = document.querySelector('.nav-item[data-view="chat"]');
  switchView('chat', agentNav);
  loadSessions();
}

var sessionListVisible = true;

function toggleSessionList() {
  sessionListVisible = !sessionListVisible;
  var section = document.getElementById('sessionSection');
  var toggle = document.getElementById('sessionToggle');
  if (sessionListVisible) {
    section.style.display = '';
    toggle.classList.add('open');
  } else {
    section.style.display = 'none';
    toggle.classList.remove('open');
  }
}

function showSessionInput() {
  document.getElementById('sessionNewBtn').style.display = 'none';
  var wrap = document.getElementById('sessionNewInput');
  wrap.style.display = 'flex';
  var input = document.getElementById('sessionNameInput');
  input.value = '';
  input.focus();
}

function cancelNewSession() {
  document.getElementById('sessionNewBtn').style.display = '';
  document.getElementById('sessionNewInput').style.display = 'none';
}

function handleSessionInputKey(e) {
  if (e.key === 'Enter') { confirmNewSession(); e.preventDefault(); }
  if (e.key === 'Escape') { cancelNewSession(); e.preventDefault(); }
}

async function confirmNewSession() {
  var input = document.getElementById('sessionNameInput');
  var name = input.value.trim();
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
  cancelNewSession();
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
