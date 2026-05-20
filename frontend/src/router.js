// Hash-based router. Register views, navigate via location.hash.

const routes = [];

export function register(pattern, render, opts = {}) {
    routes.push({ pattern, render, ...opts });
}

export function navigate(hash) {
    if (location.hash === hash) {
        dispatch();
    } else {
        location.hash = hash;
    }
}

function match(hash) {
    const h = (hash || '#/').replace(/^#/, '') || '/';
    for (const route of routes) {
        if (route.pattern instanceof RegExp) {
            const m = h.match(route.pattern);
            if (m) return { route, params: m.groups || {} };
        } else if (route.pattern === h) {
            return { route, params: {} };
        }
    }
    return null;
}

function dispatch() {
    const matched = match(location.hash);
    const view = document.getElementById('view');
    if (!matched) {
        view.replaceChildren(Object.assign(document.createElement('div'), { textContent: 'Not found' }));
        return;
    }
    matched.route.render(view, matched.params);
    renderNav();
}

function renderNav() {
    const nav = document.getElementById('nav');
    if (!nav) return;
    const links = [
        ['#/', 'Generate'],
        ['#/sessions', 'Sessions'],
        ['#/chunks', 'Chunks'],
        ['#/constructions', 'Constructions'],
        ['#/logs', 'AI Logs'],
    ];
    nav.replaceChildren(...links.map(([href, label]) => {
        const a = document.createElement('a');
        a.href = href; a.textContent = label;
        if ((location.hash || '#/') === href || (href !== '#/' && location.hash.startsWith(href))) {
            a.classList.add('active');
        }
        return a;
    }));
}

export function start() {
    window.addEventListener('hashchange', dispatch);
    if (!location.hash) location.hash = '#/';
    dispatch();
}
