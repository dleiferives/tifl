// Thin typed wrapper around fetch. One function per endpoint — add more as backend grows.

async function request(path, opts = {}) {
    const res = await fetch(path, {
        headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
        ...opts,
    });
    if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(`${res.status} ${res.statusText}: ${text}`);
    }
    if (res.status === 204) return null;
    return res.json();
}

export const api = {
    generateSession: (body) =>
        request('/api/sessions/generate', { method: 'POST', body: JSON.stringify(body) }),
    listLevels: () => request('/api/levels'),
    listTaskTypes: () => request('/api/task_types'),
    listSessions: () => request('/api/sessions'),
    getSession: (id) => request(`/api/sessions/${id}`),
    getStory: (id) => request(`/api/sessions/${id}/story`),
    getTasks: (id) => request(`/api/sessions/${id}/tasks`),
    getSessionLlmCalls: (id) => request(`/api/sessions/${id}/llm_calls`),
    submitTask: (taskId, body) =>
        request(`/api/tasks/${taskId}/submit`, { method: 'POST', body: JSON.stringify(body) }),

    listChunks: (onlyAvailable = false) =>
        request(`/api/chunks${onlyAvailable ? '?only_available=true' : ''}`),
    listConstructions: () => request('/api/constructions'),

    listLogs: () => request('/api/logs'),
    getLog: (file) => request(`/api/logs/${encodeURIComponent(file)}`),

    health: () => request('/healthz'),
};
