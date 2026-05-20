import { register, start } from './router.js';
import { render as homeView } from './views/home.js';
import { render as sessionView } from './views/session.js';
import { render as sessionsList } from './views/sessions_list.js';
import { render as chunksView } from './views/chunks.js';
import { render as constructionsView } from './views/constructions.js';
import { renderList as logsList, renderOne as logView } from './views/logs.js';

register('/', homeView);
register('/sessions', sessionsList);
register(/^\/session\/(?<id>[^/]+)$/, sessionView);
register('/chunks', chunksView);
register('/constructions', constructionsView);
register('/logs', logsList);
register(/^\/logs\/(?<file>.+)$/, logView);

start();
