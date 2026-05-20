// Minimal DOM helpers. Keeps views declarative without a framework.

export function h(tag, attrs = {}, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
        if (v == null || v === false) continue;
        if (k === 'class') el.className = v;
        else if (k === 'html') el.innerHTML = v;
        else if (k.startsWith('on') && typeof v === 'function') {
            el.addEventListener(k.slice(2).toLowerCase(), v);
        } else if (k === 'style' && typeof v === 'object') {
            Object.assign(el.style, v);
        } else {
            el.setAttribute(k, v === true ? '' : v);
        }
    }
    for (const c of children.flat()) {
        if (c == null || c === false) continue;
        el.append(c instanceof Node ? c : document.createTextNode(String(c)));
    }
    return el;
}

export function mount(root, node) {
    root.replaceChildren(node);
}

export function toast(msg, isError = false) {
    const el = document.getElementById('toast');
    if (!el) return;
    el.textContent = msg;
    el.classList.toggle('err', isError);
    el.hidden = false;
    clearTimeout(toast._t);
    toast._t = setTimeout(() => (el.hidden = true), 3500);
}

export function loading(text = 'Loading…') {
    return h('div', { class: 'muted' }, text);
}

export function errorBlock(err) {
    return h('div', { class: 'card' },
        h('h3', { class: 'err' }, 'Error'),
        h('pre', {}, err && err.message ? err.message : String(err)),
    );
}
