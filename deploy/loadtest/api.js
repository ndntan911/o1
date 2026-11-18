// Authenticated read API with one API key; the override raises the per-key
// limiter so this measures the stack. Same thresholds as the status scenario.
import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const WORKSPACE_ID = __ENV.WORKSPACE_ID;
const API_KEY = __ENV.API_KEY;

if (!WORKSPACE_ID || !API_KEY) {
	throw new Error('WORKSPACE_ID and API_KEY are required');
}

const headers = { Authorization: `Bearer ${API_KEY}` };

export const options = {
	scenarios: {
		api: {
			executor: 'constant-arrival-rate',
			rate: Number(__ENV.RATE || 100),
			timeUnit: '1s',
			duration: __ENV.DURATION || '5m',
			preAllocatedVUs: 30,
			maxVUs: 200,
		},
	},
	thresholds: {
		http_req_failed: ['rate<0.001'],
		http_req_duration: ['p(95)<300', 'p(99)<600'],
		'http_req_duration{name:monitor-list}': ['p(95)<300'],
		'http_req_duration{name:monitor-summary}': ['p(95)<300'],
	},
};

export default function () {
	const list = http.get(`${BASE}/api/v1/workspaces/${WORKSPACE_ID}/monitors`, {
		headers,
		tags: { name: 'monitor-list' },
	});
	const ok = check(list, { 'list 200': (r) => r.status === 200 });
	if (!ok) return;

	const monitors = list.json('monitors') || [];
	if (monitors.length === 0) return;

	const id = monitors[0].id;
	const summary = http.get(
		`${BASE}/api/v1/workspaces/${WORKSPACE_ID}/monitors/${id}/summary`,
		{ headers, tags: { name: 'monitor-summary' } },
	);
	check(summary, { 'summary 200': (r) => r.status === 200 });
}
