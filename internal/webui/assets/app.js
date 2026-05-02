const output = document.getElementById('output');
const toolCalls = document.getElementById('tool-calls');
const toolCount = document.getElementById('tool-count');
const inputForm = document.getElementById('input-form');
const userInput = document.getElementById('user-input');
const sendBtn = document.getElementById('send-btn');
const spinner = document.getElementById('spinner');
const statusText = document.getElementById('status-text');
const modelInfo = document.getElementById('model-info');
const connectionStatus = document.getElementById('connection-status');
const sessionStatus = document.getElementById('session-status');
const newSessionBtn = document.getElementById('new-session-btn');
const exportMdBtn = document.getElementById('export-md-btn');

let socket;
let conversation = [];
let history = [];
let historyIndex = -1;
let currentDraft = '';
let requestInFlight = false;
let tools = [];
const toolIndex = new Map();
const maxTools = 24;

function loadHistory() {
    try {
        const stored = localStorage.getItem('webui-history');
        if (stored) {
            history = JSON.parse(stored);
        }
    } catch (e) {
        console.warn('Failed to load history from localStorage:', e);
        history = [];
    }
}

function saveHistory() {
    try {
        localStorage.setItem('webui-history', JSON.stringify(history));
    } catch (e) {
        console.warn('Failed to save history to localStorage:', e);
    }
}

if (typeof marked !== 'undefined') {
    marked.setOptions({
        breaks: true,
        gfm: true,
    });
}

function shouldStickToBottom(container) {
    const threshold = 80;
    return container.scrollHeight - container.scrollTop - container.clientHeight < threshold;
}

function scrollIfNeeded(container, shouldScroll) {
    if (shouldScroll) {
        container.scrollTop = container.scrollHeight;
    }
}

function updateConnectionState(state) {
    connectionStatus.textContent = state;
    connectionStatus.classList.toggle('is-connected', state === 'Connected');
    connectionStatus.classList.toggle('is-disconnected', state !== 'Connected');
    updateSendButtonState();
}

function updateSessionState(state, busy = false) {
    sessionStatus.textContent = state;
    sessionStatus.classList.toggle('is-busy', busy);
}

function updateSendButtonState() {
    const connected = socket && socket.readyState === WebSocket.OPEN;
    sendBtn.disabled = !connected || requestInFlight;
}

function setThinking(thinking, text = '') {
    requestInFlight = thinking;
    spinner.classList.toggle('hidden', !thinking);
    statusText.textContent = text || (thinking ? 'Analyzing request...' : 'Ready for the next investigation.');
    updateSessionState(thinking ? 'Investigating' : 'Ready', thinking);
    updateSendButtonState();
}

function connect() {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    socket = new WebSocket(`${proto}//${window.location.host}/ws`);
    updateConnectionState('Connecting');
    updateSendButtonState();

    socket.onopen = () => {
        updateConnectionState('Connected');
        setThinking(false);
    };

    socket.onmessage = (event) => {
        try {
            const msg = JSON.parse(event.data);
            handleMessage(msg);
        } catch (e) {
            addMessage('error', `Failed to parse server response: ${e.message}`);
        }
    };

    socket.onclose = () => {
        updateConnectionState('Disconnected');
        setThinking(false, 'Disconnected from server. Refresh to reconnect.');
        updateSessionState('Offline');
        addMessage('error', 'Disconnected from server. Please refresh.');
    };

    socket.onerror = (error) => {
        console.error('WebSocket error:', error);
    };
}

function handleMessage(msg) {
    switch (msg.type) {
        case 'setup':
            modelInfo.textContent = msg.model || 'Unknown';
            break;
        case 'status':
            setThinking(Boolean(msg.thinking), String(msg.content || 'Analyzing request...'));
            break;
        case 'user':
            conversation.push({ role: 'user', content: msg.content || '' });
            addMessage('user', msg.content || '');
            clearToolState();
            break;
        case 'assistant':
            conversation.push({ role: 'assistant', content: msg.content || '' });
            addMessage('assistant', msg.content || '');
            break;
        case 'system':
            conversation.push({ role: 'system', content: msg.content || '' });
            addMessage('system', msg.content || '');
            break;
        case 'error':
            addMessage('error', msg.content || 'Unknown error');
            break;
        case 'tool':
            if (msg.tool) {
                upsertTool(msg.tool);
            }
            break;
        case 'clear_status':
            setThinking(false);
            break;
        default:
            console.warn('Unhandled message type:', msg.type);
    }
}

async function addMessage(type, content) {
    const shouldScroll = shouldStickToBottom(output);
    const article = document.createElement('article');
    article.className = `message ${type}-msg`;

    const header = document.createElement('div');
    header.className = 'message-header';

    const label = document.createElement('span');
    label.className = 'message-label';
    label.textContent = messageLabel(type);
    header.appendChild(label);

    const body = document.createElement('div');
    body.className = 'message-body';

    if (type === 'assistant' && typeof marked !== 'undefined') {
        try {
            body.innerHTML = await marked.parse(String(content));
            wrapRenderedTables(body);
        } catch (e) {
            body.textContent = String(content);
        }
    } else {
        body.textContent = String(content);
    }

    article.appendChild(header);
    article.appendChild(body);
    output.appendChild(article);
    scrollIfNeeded(output, shouldScroll);
}

function messageLabel(type) {
    switch (type) {
        case 'user':
            return 'You';
        case 'assistant':
            return 'Assistant';
        case 'system':
            return 'System';
        case 'error':
            return 'Error';
        default:
            return type;
    }
}

function wrapRenderedTables(root) {
    root.querySelectorAll('table').forEach((table) => {
        if (table.parentElement && table.parentElement.classList.contains('table-wrap')) {
            return;
        }
        const wrapper = document.createElement('div');
        wrapper.className = 'table-wrap';
        table.parentNode.insertBefore(wrapper, table);
        wrapper.appendChild(table);
    });
}

function clearToolState() {
    tools = [];
    toolIndex.clear();
    renderTools();
}

function upsertTool(tool) {
    const key = tool.id || `${tool.name}-${tool.seq}`;
    const existing = toolIndex.get(key);
    const payload = {
        id: tool.id || key,
        seq: tool.seq || 0,
        name: tool.name || '(unknown)',
        state: tool.state || 'running',
        args: tool.args || {},
        result: tool.result || '',
        isError: Boolean(tool.is_error),
        expanded: existing ? existing.expanded : false,
    };

    if (existing) {
        Object.assign(existing, payload);
        toolIndex.set(key, existing);
    } else {
        tools = [payload, ...tools].slice(0, maxTools);
        toolIndex.set(key, payload);
    }

    renderTools();
}

function renderTools() {
    renderToolList(toolCalls, tools, 'Tool activity will appear here.');
    toolCount.textContent = `${tools.length} total`;
}

function renderToolList(container, tools, emptyMessage) {
    const shouldScroll = shouldStickToBottom(container);
    container.innerHTML = '';

    if (tools.length === 0) {
        const empty = document.createElement('p');
        empty.className = 'empty-state';
        empty.textContent = emptyMessage;
        container.appendChild(empty);
        return;
    }

    tools.forEach((tool) => {
        const card = document.createElement('article');
        card.className = `tool-card is-${tool.state}`;
        if (tool.expanded) {
            card.classList.add('expanded');
        }

        const toggle = document.createElement('button');
        toggle.type = 'button';
        toggle.className = 'tool-card-toggle';

        const main = document.createElement('div');
        main.className = 'tool-main';

        const topLine = document.createElement('div');
        topLine.className = 'tool-topline';

        const name = document.createElement('strong');
        name.className = 'tool-name';
        name.textContent = tool.name;

        const state = document.createElement('span');
        state.className = `tool-state ${tool.state}`;
        state.textContent = tool.state;

        topLine.appendChild(name);
        topLine.appendChild(state);

        const summary = document.createElement('div');
        summary.className = 'tool-summary';
        summary.textContent = summarizeTool(tool);

        main.appendChild(topLine);
        main.appendChild(summary);

        const chevron = document.createElement('span');
        chevron.className = 'tool-chevron';
        chevron.textContent = '▶';

        toggle.appendChild(main);
        toggle.appendChild(chevron);

        const body = document.createElement('div');
        body.className = 'tool-card-body';

        body.appendChild(createToolBlock('Arguments', JSON.stringify(tool.args || {}, null, 2)));
        if (tool.result) {
            body.appendChild(createToolBlock('Result', tool.result));
        }

        toggle.addEventListener('click', () => {
            tool.expanded = !tool.expanded;
            card.classList.toggle('expanded', tool.expanded);
        });

        card.appendChild(toggle);
        card.appendChild(body);
        container.appendChild(card);
    });

    scrollIfNeeded(container, shouldScroll);
}

function createToolBlock(label, content) {
    const block = document.createElement('section');
    block.className = 'tool-block';

    const title = document.createElement('p');
    title.className = 'tool-block-label';
    title.textContent = label;

    const pre = document.createElement('pre');
    pre.textContent = content;

    block.appendChild(title);
    block.appendChild(pre);
    return block;
}

function summarizeTool(tool) {
    const keys = Object.keys(tool.args || {});
    let summary = keys.length === 0
        ? 'No arguments.'
        : keys.slice(0, 3).map((key) => `${key}: ${summarizeValue(tool.args[key])}`).join(' · ');

    if (tool.state !== 'running' && tool.result) {
        summary += ` · ${summarizeResult(tool.result)}`;
    }

    return summary;
}

function summarizeValue(value) {
    if (value === null || value === undefined) {
        return 'null';
    }
    if (Array.isArray(value)) {
        return `[${value.length}]`;
    }
    if (typeof value === 'object') {
        return '{...}';
    }
    const text = String(value).replace(/\s+/g, ' ').trim();
    return text.length > 42 ? `${text.slice(0, 39)}...` : text;
}

function summarizeResult(result) {
    const firstLine = String(result).split('\n').find((line) => line.trim()) || '';
    const normalized = firstLine.replace(/\s+/g, ' ').trim();
    if (!normalized) {
        return 'No textual result.';
    }
    return normalized.length > 72 ? `${normalized.slice(0, 69)}...` : normalized;
}

function setupHistoryHandling() {
    userInput.addEventListener('keydown', (event) => {
        if (event.key === 'ArrowUp') {
            event.preventDefault();
            if (historyIndex === -1) {
                currentDraft = userInput.value;
                historyIndex = history.length > 0 ? history.length - 1 : -1;
            } else if (historyIndex > 0) {
                historyIndex -= 1;
            }

            if (historyIndex >= 0 && historyIndex < history.length) {
                userInput.value = history[historyIndex];
            }
        } else if (event.key === 'ArrowDown') {
            event.preventDefault();
            if (historyIndex !== -1) {
                historyIndex += 1;
                if (historyIndex >= history.length) {
                    historyIndex = -1;
                    userInput.value = currentDraft;
                } else {
                    userInput.value = history[historyIndex];
                }
            }
        }
    });
}

function sendUserMessage() {
    const text = userInput.value.trim();
    if (!text || !socket || socket.readyState !== WebSocket.OPEN || requestInFlight) {
        return;
    }

    history.push(text);
    saveHistory();
    historyIndex = -1;
    currentDraft = '';
    userInput.value = '';

    socket.send(JSON.stringify({ type: 'user', content: text }));
    setThinking(true, 'Analyzing request...');
}

function startNewSession() {
    if (!socket || socket.readyState !== WebSocket.OPEN) {
        return;
    }

    output.innerHTML = '';
    conversation = [];
    clearToolState();
    historyIndex = -1;
    currentDraft = '';
    userInput.value = '';
    userInput.focus();
    setThinking(true, 'Resetting session...');
    socket.send(JSON.stringify({ type: 'reset' }));
}

function exportToMarkdown() {
    if (conversation.length === 0) {
        alert('No conversation to export.');
        return;
    }

    let md = `# Elastic Security Investigation Export\n\n`;
    md += `*Exported on: ${new Date().toLocaleString()}*\n\n---\n\n`;

    conversation.forEach((msg) => {
        const label = msg.role === 'user' ? 'You' : msg.role === 'assistant' ? 'Assistant' : 'System';
        md += `**${label}:**\n${msg.content}\n\n`;
    });

    const blob = new Blob([md], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    
    a.href = url;
    a.download = `investigation-export-${timestamp}.md`;
    document.body.appendChild(a);
    a.click();
    
    setTimeout(() => {
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }, 0);
}

inputForm.addEventListener('submit', (event) => {
    event.preventDefault();
    sendUserMessage();
});

newSessionBtn.addEventListener('click', startNewSession);
exportMdBtn.addEventListener('click', exportToMarkdown);

loadHistory();
setupHistoryHandling();
renderTools();
connect();
