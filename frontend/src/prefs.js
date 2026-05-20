// Persist the user's last-used generation config in localStorage.

const KEY = 'greek_l2_prefs_v1';

export function loadPrefs() {
    try {
        const raw = localStorage.getItem(KEY);
        if (!raw) return {};
        return JSON.parse(raw);
    } catch {
        return {};
    }
}

export function savePrefs(p) {
    try { localStorage.setItem(KEY, JSON.stringify(p)); } catch {}
}
